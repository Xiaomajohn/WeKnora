// Command admin-cancel-parser is a one-shot operational tool that cancels
// every in-progress knowledge parse across all tenants.
//
// Semantics mirror the per-knowledge POST /knowledge/:id/cancel-parse
// HTTP path (service.CancelKnowledgeParse):
//
//   - parse_status flips to "cancelled" and pending_subtasks_count → 0
//   - the active attempt's still-running spans are cancelled
//   - matching wiki task_pending_ops rows are removed so the next
//     wiki batch doesn't pick the abandoned knowledge up again
//   - queued asynq tasks for the knowledge are deleted via the inspector
//     (REDIS_ADDR must be reachable in the host environment — adjust
//     REDIS_ADDR below if running outside docker-compose).
//
// Already-written chunks/vectors are NOT touched; the user can reparse
// the knowledge later. This is the same contract as the per-row cancel.
//
// Usage:
//
//	go run ./cmd/admin-cancel-parser \
//	    -dsn "host=... user=... password=... dbname=... sslmode=disable" \
//	    -redis "redis-host:6379"
//
// Both flags are optional; with no flags the tool reads
// PG* / REDIS_ADDR-style env vars (set by `docker exec` below).
//
// This file deliberately lives outside cmd/server — it is a standalone
// Go program compiled and run on demand. It is not part of the server
// image and shares no resources with the running WeKnora-app container
// beyond the database it operates on.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Cancellable statuses mirror service.CancelKnowledgeParse's switch.
var cancellableParseStatuses = []string{
	types.ParseStatusPending,
	types.ParseStatusProcessing,
	types.ParseStatusFinalizing,
}

func main() {
	dsn := flag.String("dsn", os.Getenv("ADMIN_CANCEL_DSN"), "Postgres DSN; falls back to PG* env vars when empty")
	redisAddr := flag.String("redis", os.Getenv("ADMIN_CANCEL_REDIS"), "Redis host:port for asynq inspector; falls back to REDIS_ADDR when empty")
	redisPassword := flag.String("redis-password", os.Getenv("ADMIN_CANCEL_REDIS_PASSWORD"), "Redis password; falls back to REDIS_PASSWORD when empty")
	redisDB := flag.Int("redis-db", -1, "Redis DB index; falls back to REDIS_DB (default 0) when < 0")
	dryRun := flag.Bool("dry-run", false, "List affected knowledge IDs and exit without mutating anything")
	flag.Parse()

	cfg, err := config.LoadConfig()
	if err != nil {
		// LoadConfig also fills non-DB fields (server host/port, brand...).
		// For this tool those are irrelevant — fall back to minimal defaults
		// so a missing config.yaml on the host doesn't block the operation.
		fmt.Fprintf(os.Stderr, "[warn] LoadConfig failed (%v) — proceeding with env-driven DB only\n", err)
		cfg = &config.Config{}
	}
	_ = cfg // kept for future use (e.g. tenant allow-list filtering)
	// The logger package self-configures via init() + ConfigureFromEnv();
	// we deliberately do NOT re-init it here so the tool respects whatever
	// LOG_LEVEL / LOG_FORMAT the operator already exported.

	db, err := openDB(*dsn)
	if err != nil {
		die("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		die("get sql.DB: %v", err)
	}
	defer sqlDB.Close()

	redisClient, redisAddrUsed := openRedis(*redisAddr, *redisPassword, *redisDB)
	var asynqInspector *asynq.Inspector
	if redisClient != nil {
		asynqInspector = asynq.NewInspectorFromRedisClient(redisClient)
		defer redisClient.Close()
	}

	ctx := context.Background()
	spanRepo := repository.NewKnowledgeSpanRepository(db)

	// 1. List every knowledge with a still-active parse. Cross-tenant by
	// design — the user asked to stop all parsing system-wide.
	var inProgress []types.Knowledge
	if err := db.WithContext(ctx).
		Where("parse_status IN ?", cancellableParseStatuses).
		Where("deleted_at IS NULL").
		Select("id, tenant_id, knowledge_base_id, parse_status, summary_status, pending_subtasks_count").
		Find(&inProgress).Error; err != nil {
		die("list in-progress knowledge: %v", err)
	}

	fmt.Fprintf(os.Stdout, "Found %d in-progress knowledge row(s) (status in: %s)\n",
		len(inProgress), strings.Join(cancellableParseStatuses, ", "))
	for i := range inProgress {
		k := inProgress[i]
		fmt.Fprintf(os.Stdout, "  - %s  tenant=%d  kb=%s  parse=%s  pending_subs=%d\n",
			k.ID, k.TenantID, k.KnowledgeBaseID, k.ParseStatus, k.PendingSubtasksCount)
	}
	if *dryRun || len(inProgress) == 0 {
		if *dryRun {
			fmt.Fprintln(os.Stdout, "--dry-run set; no mutations applied")
		}
		return
	}

	// 2. Apply the cancel sequence per row. Errors are logged and
	// collected rather than failing the whole batch — the operator asked
	// to stop everything, and one stubborn row shouldn't block the rest.
	type rowResult struct {
		knowledgeID     string
		dbOK            bool
		spansCancelled  int64
		opsDeleted      int64
		asynqDequeued   int
		asynqSignalled  int
		err             string
	}
	results := make([]rowResult, 0, len(inProgress))

	for i := range inProgress {
		k := inProgress[i]
		res := rowResult{knowledgeID: k.ID}

		// 2a. Flip the row in one update so a late subtask FinalizeSubtask
		// can't race-promote it back to completed. Mirrors the per-row
		// cancel path (service.CancelKnowledgeParse).
		updates := map[string]any{
			"parse_status":           types.ParseStatusCancelled,
			"error_message":          "用户已取消解析 (admin bulk cancel)",
			"pending_subtasks_count": 0,
			"updated_at":             time.Now().UTC(),
		}
		if err := db.WithContext(ctx).Model(&types.Knowledge{}).
			Where("id = ? AND parse_status IN ?", k.ID, cancellableParseStatuses).
			Updates(updates).Error; err != nil {
			res.err = fmt.Sprintf("db update: %v", err)
			results = append(results, res)
			continue
		}
		res.dbOK = true

		// 2b. Abort the active attempt's open spans so the trace viewer
		// doesn't leave orphan "running" bars for the cancelled run.
		if attempt, err := spanRepo.LatestAttempt(ctx, k.ID); err == nil && attempt > 0 {
			if n, err := spanRepo.CancelAllOpenSpans(ctx, k.ID, attempt,
				"USER_CANCELLED", "用户已取消解析 (admin bulk cancel)"); err == nil {
				res.spansCancelled = n
			} else {
				logger.Warnf(ctx, "[admin-cancel] span abort kid=%s attempt=%d: %v", k.ID, attempt, err)
			}
			// Closing the root span so the trace ends neatly. We reuse the
			// repository's Upsert via a minimal row; if the root is already
			// terminal this is a cheap no-op.
			if root, _, err := findRootSpan(ctx, spanRepo, k.ID, attempt); err == nil && root != nil {
				now := time.Now().UTC()
				root.Status = types.SpanStatusCancelled
				root.ErrorCode = "USER_CANCELLED"
				root.ErrorMessage = "用户已取消解析 (admin bulk cancel)"
				root.FinishedAt = &now
				_ = spanRepo.Upsert(ctx, root)
			}
		} else if err != nil {
			logger.Warnf(ctx, "[admin-cancel] latest attempt lookup kid=%s: %v", k.ID, err)
		}

		// 2c. Drop matching wiki task_pending_ops rows so the 30s wiki
		// batch doesn't burn an LLM call on a doc the operator abandoned.
		// Knowledge row doesn't carry a tenant_id-side constraint on the
		// ops table, so filter by dedup_key = knowledge_id (the queue
		// uses the knowledge ID as the wiki-ingest dedup key).
		opsRes := db.WithContext(ctx).
			Where("task_type = ? AND scope = ? AND dedup_key = ?",
				types.TypeWikiIngest, types.TaskScopeKnowledgeBase, k.ID).
			Delete(&types.TaskPendingOp{})
		if opsRes.Error != nil {
			logger.Warnf(ctx, "[admin-cancel] wiki ops scrub kid=%s: %v", k.ID, opsRes.Error)
		} else {
			res.opsDeleted = opsRes.RowsAffected
		}

		// 2d. Best-effort asynq dequeue + active-signal via the inspector.
		// Mirrors the per-row CancelTasksForKnowledge sweep across every
		// queue the parsing pipeline lands in.
		if asynqInspector != nil {
			deleted, cancelled, err := sweepAsynqForKnowledge(ctx, asynqInspector, k.ID)
			if err != nil {
				logger.Warnf(ctx, "[admin-cancel] asynq sweep kid=%s: %v", k.ID, err)
			}
			res.asynqDequeued = deleted
			res.asynqSignalled = cancelled
		}

		results = append(results, res)
	}

	// 3. Tally + report.
	var (
		dbOK, spansOK, opsOK, asynqOK int
		errorCount                    int
	)
	for _, r := range results {
		if r.dbOK {
			dbOK++
		}
		if r.spansCancelled > 0 {
			spansOK++
		}
		if r.opsDeleted > 0 {
			opsOK++
		}
		if r.asynqDequeued > 0 || r.asynqSignalled > 0 {
			asynqOK++
		}
		if r.err != "" {
			errorCount++
			fmt.Fprintf(os.Stderr, "[error] kid=%s: %s\n", r.knowledgeID, r.err)
		}
	}

	asynqNote := "skipped (no REDIS_ADDR)"
	if asynqInspector != nil {
		asynqNote = fmt.Sprintf("via %s", redisAddrUsed)
	}

	fmt.Fprintln(os.Stdout, "---")
	fmt.Fprintf(os.Stdout, "Cancel summary:\n")
	fmt.Fprintf(os.Stdout, "  knowledge rows flipped to cancelled : %d / %d\n", dbOK, len(inProgress))
	fmt.Fprintf(os.Stdout, "  attempts with open spans aborted   : %d / %d\n", spansOK, len(inProgress))
	fmt.Fprintf(os.Stdout, "  knowledge ids w/ wiki ops scrubbed : %d / %d\n", opsOK, len(inProgress))
	fmt.Fprintf(os.Stdout, "  asynq sweep (dequeue+signal) rows  : %d / %d (%s)\n", asynqOK, len(inProgress), asynqNote)
	fmt.Fprintf(os.Stdout, "  errors                              : %d\n", errorCount)
}

// openDB builds a gorm.DB connection from a DSN string. We deliberately
// avoid touching the application's migration pipeline — this tool runs
// against an already-migrated database.
func openDB(dsn string) (*gorm.DB, error) {
	if dsn == "" {
		// Fall back to the same env vars the server uses, but with a
		// host remap so a `go run` from the project root reaches the
		// dockerised postgres on the docker bridge IP.
		host := os.Getenv("DB_HOST")
		if host == "" {
			host = "localhost"
		} else if host == "postgres" {
			// Service name resolves inside docker-compose but not on the
			// host. Prefer the host-side bridge IP we discovered.
			if v := os.Getenv("ADMIN_CANCEL_DB_HOST"); v != "" {
				host = v
			}
		}
		port := envOr("DB_PORT", "5432")
		user := envOr("DB_USER", "postgres")
		password := os.Getenv("DB_PASSWORD")
		dbname := envOr("DB_NAME", "WeKnora")
		dsn = fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable TimeZone=UTC",
			host, port, user, password, dbname,
		)
	}
	return gorm.Open(postgres.Open(dsn), &gorm.Config{
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
}

func openRedis(addr, password string, dbIndex int) (*redis.Client, string) {
	if addr == "" {
		addr = os.Getenv("REDIS_ADDR")
	}
	if addr == "" {
		return nil, ""
	}
	if password == "" {
		password = os.Getenv("REDIS_PASSWORD")
	}
	db := dbIndex
	if db < 0 {
		if v := os.Getenv("REDIS_DB"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				db = n
			}
		}
	}
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] redis ping %s failed (%v) — asynq sweep will be skipped\n", addr, err)
		_ = client.Close()
		return nil, addr
	}
	return client, addr
}

// taskTypesForAdminCancel mirrors router.taskTypesForKnowledgeCancel.
// Kept narrow on purpose: we only cancel parsing tasks, not KB-level
// sync jobs or unrelated work.
var taskTypesForAdminCancel = map[string]struct{}{
	types.TypeDocumentProcess:      {},
	types.TypeManualProcess:        {},
	types.TypeImageMultimodal:      {},
	types.TypeKnowledgePostProcess: {},
	types.TypeQuestionGeneration:   {},
	types.TypeSummaryGeneration:    {},
	types.TypeChunkExtract:         {},
}

// queuesForAdminCancel returns every queue the parsing pipeline can land
// in. Mirrors router.queuesScanned but is computed inline so this binary
// carries no dig/wire dependency on the server's router package.
func queuesForAdminCancel() []string {
	definitions := types.QueueDefinitions()
	queues := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		queues = append(queues, definition.Name)
	}
	return queues
}

// sweepAsynqForKnowledge walks every queue the parsing pipeline uses
// and removes (or signals) any task whose payload references knowledgeID.
// Returns (deleted_from_queue, cancelled_active, error).
func sweepAsynqForKnowledge(ctx context.Context, insp *asynq.Inspector, knowledgeID string) (int, int, error) {
	queues := queuesForAdminCancel()
	deleted := 0
	cancelled := 0

	for _, queue := range queues {
		d, err := deleteFromState(ctx, insp, queue, knowledgeID, asynq.TaskStatePending, insp.ListPendingTasks)
		if err != nil {
			return deleted, cancelled, err
		}
		deleted += d

		d, err = deleteFromState(ctx, insp, queue, knowledgeID, asynq.TaskStateScheduled, insp.ListScheduledTasks)
		if err != nil {
			return deleted, cancelled, err
		}
		deleted += d

		d, err = deleteFromState(ctx, insp, queue, knowledgeID, asynq.TaskStateRetry, insp.ListRetryTasks)
		if err != nil {
			return deleted, cancelled, err
		}
		deleted += d

		c, err := cancelActive(ctx, insp, queue, knowledgeID)
		if err != nil {
			return deleted, cancelled, err
		}
		cancelled += c
	}
	return deleted, cancelled, nil
}

// matchesKnowledge is a value-only check: empty knowledgeIDs never
// match, the task type must be in our cancellation allow-list, and the
// payload's knowledge_id field (read by the probe struct) must equal
// the target. JSON errors count as "no match" since the inspection
// list is best-effort by design.
type knowledgeIDProbe struct {
	KnowledgeID string `json:"knowledge_id,omitempty"`
}

func matchesKnowledge(t *asynq.TaskInfo, knowledgeID string) bool {
	if _, ok := taskTypesForAdminCancel[t.Type]; !ok {
		return false
	}
	var p knowledgeIDProbe
	if err := json.Unmarshal(t.Payload, &p); err != nil {
		return false
	}
	return p.KnowledgeID == knowledgeID
}

func deleteFromState(
	ctx context.Context,
	insp *asynq.Inspector,
	queue, knowledgeID string,
	state asynq.TaskState,
	listFn func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error),
) (int, error) {
	const pageSize = 100
	deleted := 0
	for page := 1; ; page++ {
		tasks, err := listFn(queue, asynq.PageSize(pageSize), asynq.Page(page))
		if err != nil {
			if isQueueNotFoundErr(err) {
				return deleted, nil
			}
			logger.Warnf(ctx, "[admin-cancel] list %s queue=%s page=%d: %v", state, queue, page, err)
			return deleted, nil
		}
		if len(tasks) == 0 {
			return deleted, nil
		}
		for _, t := range tasks {
			if !matchesKnowledge(t, knowledgeID) {
				continue
			}
			if err := insp.DeleteTask(queue, t.ID); err != nil {
				logger.Warnf(ctx, "[admin-cancel] delete %s id=%s: %v", state, t.ID, err)
				continue
			}
			deleted++
		}
		if len(tasks) < pageSize {
			return deleted, nil
		}
	}
}

func cancelActive(ctx context.Context, insp *asynq.Inspector, queue, knowledgeID string) (int, error) {
	const pageSize = 100
	cancelled := 0
	for page := 1; ; page++ {
		tasks, err := insp.ListActiveTasks(queue, asynq.PageSize(pageSize), asynq.Page(page))
		if err != nil {
			if isQueueNotFoundErr(err) {
				return cancelled, nil
			}
			logger.Warnf(ctx, "[admin-cancel] list active queue=%s page=%d: %v", queue, page, err)
			return cancelled, nil
		}
		if len(tasks) == 0 {
			return cancelled, nil
		}
		for _, t := range tasks {
			if !matchesKnowledge(t, knowledgeID) {
				continue
			}
			if err := insp.CancelProcessing(t.ID); err != nil {
				logger.Warnf(ctx, "[admin-cancel] cancel active id=%s: %v", t.ID, err)
				continue
			}
			cancelled++
		}
		if len(tasks) < pageSize {
			return cancelled, nil
		}
	}
}

// findRootSpan loads the attempt's span rows and returns the root span
// (kind=root) if any. A nil return indicates the tracker was never
// opened for this attempt (older knowledge rows) — closing the root is
// then a no-op which is the desired behaviour.
func findRootSpan(
	ctx context.Context,
	spanRepo repository.KnowledgeSpanRepository,
	knowledgeID string,
	attempt int,
) (*types.KnowledgeProcessingSpan, int, error) {
	rows, err := spanRepo.ListByAttempt(ctx, knowledgeID, attempt)
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		if rows[i].Kind == types.SpanKindRoot {
			r := rows[i]
			return &r, attempt, nil
		}
	}
	return nil, attempt, nil
}

// die prints an error to stderr and exits non-zero. Helper kept simple
// so a missing logger config (see main) doesn't trigger a noisy stack
// from os.Exit.
func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[fatal] "+format+"\n", args...)
	os.Exit(1)
}

// envOr returns os.Getenv(key) or fallback when empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// isQueueNotFoundErr swallows asynq.ErrQueueNotFound so a queue with no
// pending tasks doesn't abort an otherwise-successful sweep.
func isQueueNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	return err == asynq.ErrQueueNotFound || strings.Contains(err.Error(), "queue not found")
}
