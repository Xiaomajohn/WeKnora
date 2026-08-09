// Command admin-cleanup-orphans is a one-shot operational tool that deep-cleans
// every row of orphan data left behind by soft-deleted knowledge bases:
//
//   - Asynq tasks across every queue/state (pending/scheduled/retry/active AND
//     archived/failed) whose payload references one of the deleted KB IDs.
//   - Qdrant vectors — defaults to per-KB filter deletion (preserves active KBs)
//     but also accepts -flush-qdrant to nuke the entire collection, which is
//     the "reset everything" lever for the docker-compose dev environment.
//   - Postgres rows still left over from the soft-delete: knowledges, chunks,
//     embeddings, knowledge_processing_spans, knowledge_tag_relations,
//     kb_shares, user_kb_pins, task_pending_ops.
//
// Unlike admin-kb-rebuild, this tool does NOT recreate the KB. The use case
// is operational hygiene after the operator has manually deleted KBs and
// wants zero residue. Re-importing the original documents produces a fresh
// UUID KB via the normal pipeline.
//
// Scope is selectable:
//   -kb-days N    only sweep KBs whose deleted_at is within the last N days
//                 (default 7 — matches the "recent 7 days" operational window)
//   -all-kbs      sweep every deleted KB regardless of when it was deleted
//
// Dry-run: prints the plan; pass -yes to actually mutate.
//
// Usage:
//
//	go run ./cmd/admin-cleanup-orphans            # default dry-run, last 7 days
//	go run ./cmd/admin-cleanup-orphans -yes       # actually execute
//	go run ./cmd/admin-cleanup-orphans -flush-qdrant -yes  # also wipe the whole collection
//	go run ./cmd/admin-cleanup-orphans -all-kbs -yes       # sweep every deleted KB
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

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
	"github.com/qdrant/go-client/qdrant"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// kbPayloadProbe reads the canonical KB identifier from any asynq task payload.
// We accept either the legacy `knowledge_base_id` field or the alternate `kb_id`
// used by some KB-level sync tasks (mirror admin-kb-rebuild).
type kbPayloadProbe struct {
	KnowledgeBaseID string `json:"knowledge_base_id"`
	KBID            string `json:"kb_id"`
}

// cancelableTaskTypes mirrors admin-kb-rebuild's allow-list. We only target
// tasks belonging to the parsing / enrichment / sync / wiki pipeline; unrelated
// background work (e.g. session cleanup) is left untouched.
var cancelableTaskTypes = map[string]struct{}{
	types.TypeDocumentProcess:      {},
	types.TypeManualProcess:        {},
	types.TypeImageMultimodal:      {},
	types.TypeKnowledgePostProcess: {},
	types.TypeQuestionGeneration:   {},
	types.TypeSummaryGeneration:    {},
	types.TypeChunkExtract:         {},
	types.TypeDataSourceSync:       {},
	types.TypeWikiIngest:           {},
	types.TypeWikiFinalize:         {},
	types.TypeFAQImport:            {},
	types.TypeKBClone:              {},
	types.TypeIndexDelete:          {},
	types.TypeKBDelete:             {},
	types.TypeKnowledgeListDelete:  {},
	types.TypeKnowledgeListReparse: {},
	types.TypeKnowledgeMove:        {},
	types.TypeDataTableSummary:     {},
}

// perKBStats stores the per-state delete counts for a single KB so the
// summary report can show what was actually removed.
type perKBStats struct {
	pending   int
	scheduled int
	retry     int
	active    int
	archived  int
	completed int
	qdrant    int
	dbRows    int64
}

func main() {
	kbDays := flag.Int("kb-days", 7, "only sweep KBs deleted within the last N days (default 7)")
	allKBs := flag.Bool("all-kbs", false, "sweep every soft-deleted KB regardless of age")
	flushQdrant := flag.Bool("flush-qdrant", false, "DROP every point in every matching Qdrant collection (irreversible; affects active KBs too)")
	yes := flag.Bool("yes", false, "perform destructive ops (default: dry-run)")
	flag.Parse()

	if *kbDays < 0 {
		die("-kb-days must be >= 0")
	}

	if _, err := config.LoadConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] config.LoadConfig failed (%v) — proceeding with env-driven DB only\n", err)
	}

	db, err := openDB()
	if err != nil {
		die("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		die("get sql.DB: %v", err)
	}
	defer sqlDB.Close()

	redisClient, redisAddrUsed := openRedis()
	var insp *asynq.Inspector
	if redisClient != nil {
		insp = asynq.NewInspectorFromRedisClient(redisClient)
		defer redisClient.Close()
	}
	_ = redisAddrUsed

	ctx := context.Background()

	// 1. Load every deleted KB that matches the operator's filter.
	deletedKBs, err := loadDeletedKBs(ctx, db, *allKBs, *kbDays)
	if err != nil {
		die("load deleted KBs: %v", err)
	}
	if len(deletedKBs) == 0 {
		fmt.Fprintln(os.Stdout, "No deleted KBs matched the filter. Nothing to do.")
		return
	}

	fmt.Fprintf(os.Stdout, "=== Orphan cleanup plan ===\n")
	fmt.Fprintf(os.Stdout, "  filter          : %s\n", filterDescription(*allKBs, *kbDays))
	fmt.Fprintf(os.Stdout, "  deleted KBs     : %d\n", len(deletedKBs))
	fmt.Fprintf(os.Stdout, "  flush_qdrant    : %v\n", *flushQdrant)
	fmt.Fprintf(os.Stdout, "  asynq sweep     : %s\n", asynqSweepDescription(insp))
	fmt.Fprintf(os.Stdout, "  qdrant sweep    : %s\n", qdrantSweepDescription())
	fmt.Fprintf(os.Stdout, "\n  Deleted KBs in scope:\n")
	for i, kb := range deletedKBs {
		fmt.Fprintf(os.Stdout, "    [%2d] %s  %-40s  tenant=%d  deleted_at=%s\n",
			i+1, kb.ID, truncate(kb.Name, 40), kb.TenantID, kb.DeletedAt.Time.UTC().Format(time.RFC3339))
	}

	// 2. Per-KB inventory pass (read-only). We do this even on dry-run so the
	// operator sees real numbers before pulling the trigger.
	stats := make(map[string]*perKBStats, len(deletedKBs))
	totalDBRows := int64(0)
	for _, kb := range deletedKBs {
		s := &perKBStats{}
		stats[kb.ID] = s

		if insp != nil {
			var err error
			s.pending, err = countAsynqForKB(ctx, insp, kb.ID, insp.ListPendingTasks)
			if err != nil {
				logger.Warnf(ctx, "[admin-cleanup-orphans] list pending kb=%s: %v", kb.ID, err)
			}
			s.scheduled, err = countAsynqForKB(ctx, insp, kb.ID, insp.ListScheduledTasks)
			if err != nil {
				logger.Warnf(ctx, "[admin-cleanup-orphans] list scheduled kb=%s: %v", kb.ID, err)
			}
			s.retry, err = countAsynqForKB(ctx, insp, kb.ID, insp.ListRetryTasks)
			if err != nil {
				logger.Warnf(ctx, "[admin-cleanup-orphans] list retry kb=%s: %v", kb.ID, err)
			}
			s.active, err = countActiveAsynqForKB(ctx, insp, kb.ID)
			if err != nil {
				logger.Warnf(ctx, "[admin-cleanup-orphans] list active kb=%s: %v", kb.ID, err)
			}
			s.archived, err = countAsynqForKB(ctx, insp, kb.ID, insp.ListArchivedTasks)
			if err != nil {
				logger.Warnf(ctx, "[admin-cleanup-orphans] list archived kb=%s: %v", kb.ID, err)
			}
			s.completed, err = countAsynqForKB(ctx, insp, kb.ID, insp.ListCompletedTasks)
			if err != nil {
				logger.Warnf(ctx, "[admin-cleanup-orphans] list completed kb=%s: %v", kb.ID, err)
			}
		}

		// DB row counts: the soft-delete leaves children behind.
		var n int64
		row := db.WithContext(ctx).Raw(`
SELECT
  (SELECT count(*) FROM embeddings WHERE knowledge_base_id = ?) +
  (SELECT count(*) FROM chunks WHERE knowledge_base_id = ?) +
  (SELECT count(*) FROM knowledge_processing_spans WHERE knowledge_id IN (SELECT id FROM knowledges WHERE knowledge_base_id = ?)) +
  (SELECT count(*) FROM knowledge_tag_relations WHERE knowledge_id IN (SELECT id FROM knowledges WHERE knowledge_base_id = ?)) +
  (SELECT count(*) FROM kb_shares WHERE knowledge_base_id = ?) +
  (SELECT count(*) FROM user_kb_pins WHERE kb_id = ?) +
  (SELECT count(*) FROM task_pending_ops WHERE scope = ? AND scope_id = ? OR (task_type = ? AND dedup_key IN (SELECT id FROM knowledges WHERE knowledge_base_id = ?)))
`, kb.ID, kb.ID, kb.ID, kb.ID, kb.ID, kb.ID, types.TaskScopeKnowledgeBase, kb.ID, types.TypeWikiIngest, kb.ID).Row()
		if err := row.Scan(&n); err != nil {
			logger.Warnf(ctx, "[admin-cleanup-orphans] db row count kb=%s: %v", kb.ID, err)
		}
		s.dbRows = n
		totalDBRows += n
	}

	fmt.Fprintf(os.Stdout, "\n  Inventory (per KB):\n")
	fmt.Fprintf(os.Stdout, "    %-36s  %-7s %-7s %-7s %-7s %-7s %-7s %-7s %s\n",
		"kb_id(8)", "pending", "sched", "retry", "active", "archiv", "complet", "qdelet", "dbRows")
	for _, kb := range deletedKBs {
		s := stats[kb.ID]
		shortID := kb.ID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		fmt.Fprintf(os.Stdout, "    %-36s  %-7d %-7d %-7d %-7d %-7d %-7d %-7d %d\n",
			shortID, s.pending, s.scheduled, s.retry, s.active, s.archived, s.completed, s.qdrant, s.dbRows)
	}

	// 3. Qdrant plan: either per-KB filter deletion or full flush.
	var qdrantCollections []string
	var qdrantClient *qdrant.Client
	if qd := openQdrant(); qd != nil {
		qdrantClient = qd
		qdrantCollections = qdMatchingCollections(ctx, qd)
		fmt.Fprintf(os.Stdout, "\n  qdrant collections in scope: %s\n", strings.Join(qdrantCollections, ", "))
		if *flushQdrant {
			// Count once so the dry-run shows the actual blast radius.
			totalQD := int64(0)
			for _, c := range qdrantCollections {
				n, err := qdCountAll(ctx, qd, c)
				if err != nil {
					logger.Warnf(ctx, "[admin-cleanup-orphans] qdrant count %s: %v", c, err)
					continue
				}
				totalQD += n
			}
			fmt.Fprintf(os.Stdout, "  qdrant FLUSH will delete ALL points in those collections: %d\n", totalQD)
		} else {
			for _, kb := range deletedKBs {
				for _, c := range qdrantCollections {
					n, err := qdCountByKB(ctx, qd, c, kb.ID)
					if err != nil {
						logger.Warnf(ctx, "[admin-cleanup-orphans] qdrant count %s kb=%s: %v", c, kb.ID, err)
						continue
					}
					stats[kb.ID].qdrant += n
				}
			}
		}
	} else {
		fmt.Fprintf(os.Stdout, "\n  qdrant sweep skipped (no QDRANT_HOST reachable)\n")
	}

	if !*yes {
		fmt.Fprintf(os.Stdout, "\nDRY-RUN: re-run with -yes to hard-delete the above. Pass -flush-qdrant to also wipe the entire collection.\n")
		return
	}

	// 4. Destructive phase.
	fmt.Fprintf(os.Stdout, "\n=== Executing cleanup ===\n")
	totalAsynqDeleted := 0
	totalAsynqCancelled := 0
	totalDBDeleted := int64(0)
	totalQdrantDeleted := int64(0)

	for _, kb := range deletedKBs {
		fmt.Fprintf(os.Stdout, "\n  -> kb=%s (%s)\n", kb.ID, truncate(kb.Name, 40))
		s := stats[kb.ID]

		// 4a. Asynq sweep across every state.
		if insp != nil {
			d, c, err := sweepAsynqForKB(ctx, insp, kb.ID)
			if err != nil {
				logger.Warnf(ctx, "[admin-cleanup-orphans] asynq sweep kb=%s: %v", kb.ID, err)
			} else {
				totalAsynqDeleted += d
				totalAsynqCancelled += c
				fmt.Fprintf(os.Stdout, "     asynq: dequeued=%d active_cancelled=%d\n", d, c)
			}
		}

		// 4b. Qdrant: per-KB filter or full flush.
		if qdrantClient != nil {
			if *flushQdrant {
				// We honour the per-KB loop iteration only as a progress marker;
				// the actual flush is done once at the end to avoid double-counting.
				continue
			}
			for _, c := range qdrantCollections {
				n, err := qdDeleteByKB(ctx, qdrantClient, c, kb.ID)
				if err != nil {
					logger.Warnf(ctx, "[admin-cleanup-orphans] qdrant delete %s kb=%s: %v", c, kb.ID, err)
					continue
				}
				totalQdrantDeleted += int64(n)
				fmt.Fprintf(os.Stdout, "     qdrant[%s]: points_deleted=%d\n", c, n)
			}
		}

		// 4c. Postgres residue.
		deleted, err := purgeKBResidue(ctx, db, kb.ID)
		if err != nil {
			logger.Warnf(ctx, "[admin-cleanup-orphans] db purge kb=%s: %v", kb.ID, err)
			fmt.Fprintf(os.Stdout, "     db: ERROR %v\n", err)
		} else {
			totalDBDeleted += deleted
			fmt.Fprintf(os.Stdout, "     db: rows_deleted=%d\n", deleted)
		}
		_ = s
	}

	// 4d. Full Qdrant flush (if requested) — done exactly once at the end.
	if *flushQdrant && qdrantClient != nil {
		fmt.Fprintf(os.Stdout, "\n  -> qdrant FLUSH\n")
		for _, c := range qdrantCollections {
			n, err := qdFlushCollection(ctx, qdrantClient, c)
			if err != nil {
				logger.Warnf(ctx, "[admin-cleanup-orphans] qdrant flush %s: %v", c, err)
				fmt.Fprintf(os.Stdout, "     qdrant[%s]: FLUSH ERROR %v\n", c, err)
				continue
			}
			totalQdrantDeleted += n
			fmt.Fprintf(os.Stdout, "     qdrant[%s]: points_flushed=%d\n", c, n)
		}
	}

	fmt.Fprintf(os.Stdout, "\n=== Cleanup complete ===\n")
	fmt.Fprintf(os.Stdout, "  KBs processed            : %d\n", len(deletedKBs))
	fmt.Fprintf(os.Stdout, "  asynq tasks dequeued     : %d\n", totalAsynqDeleted)
	fmt.Fprintf(os.Stdout, "  asynq active cancelled   : %d\n", totalAsynqCancelled)
	fmt.Fprintf(os.Stdout, "  qdrant points removed    : %d\n", totalQdrantDeleted)
	fmt.Fprintf(os.Stdout, "  postgres rows removed    : %d\n", totalDBDeleted)
}

// loadDeletedKBs enumerates every soft-deleted KB matching the operator's
// filter. Unscoped so soft-deleted rows are visible (the standard GORM
// "Where(...)" hides them by default).
func loadDeletedKBs(ctx context.Context, db *gorm.DB, all bool, days int) ([]types.KnowledgeBase, error) {
	q := db.WithContext(ctx).Unscoped().Where("deleted_at IS NOT NULL")
	if !all {
		cutoff := time.Now().UTC().AddDate(0, 0, -days)
		q = q.Where("deleted_at >= ?", cutoff)
	}
	var rows []types.KnowledgeBase
	if err := q.Order("deleted_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// sweepAsynqForKB removes every task whose payload references kbID across
// every parsing/enrichment queue. Includes archived (failed) tasks, which is
// the entire point of this tool vs. admin-kb-rebuild.
func sweepAsynqForKB(ctx context.Context, insp *asynq.Inspector, kbID string) (int, int, error) {
	queues := kbQueueNames()
	deleted := 0
	cancelled := 0

	for _, queue := range queues {
		// In-queue states: pending, scheduled, retry — straight delete.
		d, err := kbDeleteFromState(ctx, insp, queue, kbID, insp.ListPendingTasks)
		if err != nil {
			return deleted, cancelled, err
		}
		deleted += d

		d, err = kbDeleteFromState(ctx, insp, queue, kbID, insp.ListScheduledTasks)
		if err != nil {
			return deleted, cancelled, err
		}
		deleted += d

		d, err = kbDeleteFromState(ctx, insp, queue, kbID, insp.ListRetryTasks)
		if err != nil {
			return deleted, cancelled, err
		}
		deleted += d

		// Archived (= failed/dead-letter) — the operator's primary pain point.
		d, err = kbDeleteFromState(ctx, insp, queue, kbID, insp.ListArchivedTasks)
		if err != nil {
			return deleted, cancelled, err
		}
		deleted += d

		// Completed — also stale data, delete for completeness.
		d, err = kbDeleteFromState(ctx, insp, queue, kbID, insp.ListCompletedTasks)
		if err != nil {
			return deleted, cancelled, err
		}
		deleted += d

		// Active — cancel signal so the worker stops mid-flight.
		c, err := kbCancelActive(ctx, insp, queue, kbID)
		if err != nil {
			return deleted, cancelled, err
		}
		cancelled += c
	}
	return deleted, cancelled, nil
}

func kbQueueNames() []string {
	defs := types.QueueDefinitions()
	queues := make([]string, 0, len(defs))
	for _, def := range defs {
		queues = append(queues, def.Name)
	}
	return queues
}

// countAsynqForKB counts matching tasks in a queue state without mutating.
// Used by the dry-run inventory.
func countAsynqForKB(
	ctx context.Context,
	insp *asynq.Inspector,
	kbID string,
	listFn func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error),
) (int, error) {
	const pageSize = 100
	total := 0
	queues := kbQueueNames()
	for _, q := range queues {
		for page := 1; ; page++ {
			tasks, err := listFn(q, asynq.PageSize(pageSize), asynq.Page(page))
			if err != nil {
				if isQueueNotFoundErr(err) {
					return total, nil
				}
				logger.Warnf(ctx, "[admin-cleanup-orphans] count queue=%s page=%d: %v", q, page, err)
				return total, nil
			}
			if len(tasks) == 0 {
				break
			}
			for _, t := range tasks {
				if kbMatchesTask(t, kbID) {
					total++
				}
			}
			if len(tasks) < pageSize {
				break
			}
		}
	}
	return total, nil
}

// countActiveAsynqForKB counts active tasks the same way as countAsynqForKB
// but goes through ListActiveTasks (active is a separate state in the
// inspector API).
func countActiveAsynqForKB(ctx context.Context, insp *asynq.Inspector, kbID string) (int, error) {
	const pageSize = 100
	total := 0
	queues := kbQueueNames()
	for _, q := range queues {
		for page := 1; ; page++ {
			tasks, err := insp.ListActiveTasks(q, asynq.PageSize(pageSize), asynq.Page(page))
			if err != nil {
				if isQueueNotFoundErr(err) {
					return total, nil
				}
				logger.Warnf(ctx, "[admin-cleanup-orphans] count active queue=%s page=%d: %v", q, page, err)
				return total, nil
			}
			if len(tasks) == 0 {
				break
			}
			for _, t := range tasks {
				if kbMatchesTask(t, kbID) {
					total++
				}
			}
			if len(tasks) < pageSize {
				break
			}
		}
	}
	return total, nil
}

func kbDeleteFromState(
	ctx context.Context,
	insp *asynq.Inspector,
	queue, kbID string,
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
			logger.Warnf(ctx, "[admin-cleanup-orphans] list state queue=%s page=%d: %v", queue, page, err)
			return deleted, nil
		}
		if len(tasks) == 0 {
			return deleted, nil
		}
		for _, t := range tasks {
			if !kbMatchesTask(t, kbID) {
				continue
			}
			if err := insp.DeleteTask(queue, t.ID); err != nil {
				logger.Warnf(ctx, "[admin-cleanup-orphans] delete id=%s: %v", t.ID, err)
				continue
			}
			deleted++
		}
		if len(tasks) < pageSize {
			return deleted, nil
		}
	}
}

func kbCancelActive(ctx context.Context, insp *asynq.Inspector, queue, kbID string) (int, error) {
	const pageSize = 100
	cancelled := 0
	for page := 1; ; page++ {
		tasks, err := insp.ListActiveTasks(queue, asynq.PageSize(pageSize), asynq.Page(page))
		if err != nil {
			if isQueueNotFoundErr(err) {
				return cancelled, nil
			}
			logger.Warnf(ctx, "[admin-cleanup-orphans] list active queue=%s page=%d: %v", queue, page, err)
			return cancelled, nil
		}
		if len(tasks) == 0 {
			return cancelled, nil
		}
		for _, t := range tasks {
			if !kbMatchesTask(t, kbID) {
				continue
			}
			if err := insp.CancelProcessing(t.ID); err != nil {
				logger.Warnf(ctx, "[admin-cleanup-orphans] cancel id=%s: %v", t.ID, err)
				continue
			}
			cancelled++
		}
		if len(tasks) < pageSize {
			return cancelled, nil
		}
	}
}

func kbMatchesTask(t *asynq.TaskInfo, kbID string) bool {
	if _, ok := cancelableTaskTypes[t.Type]; !ok {
		return false
	}
	var p kbPayloadProbe
	if err := json.Unmarshal(t.Payload, &p); err != nil {
		return false
	}
	return p.KnowledgeBaseID == kbID || p.KBID == kbID
}

// purgeKBResidue removes every row still tied to the soft-deleted KB. This
// mirrors admin-kb-rebuild's hard-delete pass but stops short of re-inserting
// the KB row — the operator asked for cleanup, not reconstruction.
func purgeKBResidue(ctx context.Context, db *gorm.DB, kbID string) (int64, error) {
	var total int64
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		steps := []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM embeddings WHERE knowledge_base_id = ?`, []any{kbID}},
			{`DELETE FROM chunks WHERE knowledge_base_id = ?`, []any{kbID}},
			{`DELETE FROM knowledge_processing_spans WHERE knowledge_id IN (SELECT id FROM knowledges WHERE knowledge_base_id = ?)`, []any{kbID}},
			{`DELETE FROM knowledge_tag_relations WHERE knowledge_id IN (SELECT id FROM knowledges WHERE knowledge_base_id = ?)`, []any{kbID}},
			{`DELETE FROM kb_shares WHERE knowledge_base_id = ?`, []any{kbID}},
			{`DELETE FROM user_kb_pins WHERE kb_id = ?`, []any{kbID}},
			{
				`DELETE FROM task_pending_ops WHERE scope = ? AND scope_id = ? OR (task_type = ? AND dedup_key IN (SELECT id FROM knowledges WHERE knowledge_base_id = ?))`,
				[]any{types.TaskScopeKnowledgeBase, kbID, types.TypeWikiIngest, kbID},
			},
		}
		for _, step := range steps {
			res := tx.Exec(step.sql, step.args...)
			if res.Error != nil {
				return fmt.Errorf("step %q: %w", step.sql, res.Error)
			}
			total += res.RowsAffected
		}
		// Last: knowledges themselves (Unscoped so soft-deleted rows are
		// visible — the operator's soft-delete left them around).
		res := tx.Unscoped().Where("knowledge_base_id = ?", kbID).Delete(&types.Knowledge{})
		if res.Error != nil {
			return fmt.Errorf("delete knowledges: %w", res.Error)
		}
		total += res.RowsAffected
		return nil
	})
	if err != nil {
		return total, err
	}
	return total, nil
}

func openDB() (*gorm.DB, error) {
	host := os.Getenv("DB_HOST")
	if host == "" {
		host = "localhost"
	} else if host == "postgres" {
		if v := os.Getenv("ADMIN_CANCEL_DB_HOST"); v != "" {
			host = v
		}
	}
	port := envOr("DB_PORT", "5432")
	user := envOr("DB_USER", "postgres")
	password := os.Getenv("DB_PASSWORD")
	dbname := envOr("DB_NAME", "WeKnora")
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable TimeZone=UTC",
		host, port, user, password, dbname,
	)
	return gorm.Open(postgres.Open(dsn), &gorm.Config{
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
}

func openRedis() (*redis.Client, string) {
	addr := envOr("REDIS_ADDR", os.Getenv("ADMIN_CANCEL_REDIS"))
	if addr == "" {
		return nil, ""
	}
	password := envOr("REDIS_PASSWORD", os.Getenv("ADMIN_CANCEL_REDIS_PASSWORD"))
	db := -1
	if v := os.Getenv("REDIS_DB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			db = n
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

func openQdrant() *qdrant.Client {
	host := envOr("QDRANT_HOST", "localhost")
	port, _ := strconv.Atoi(envOr("QDRANT_PORT", "6334"))
	if port == 0 {
		port = 6334
	}
	apiKey := os.Getenv("QDRANT_API_KEY")
	useTLS := strings.EqualFold(os.Getenv("QDRANT_USE_TLS"), "true")

	connCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := qdrant.NewClient(&qdrant.Config{Host: host, Port: port, APIKey: apiKey, UseTLS: useTLS})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[warn] qdrant dial %s:%d failed (%v) — vector sweep will be skipped\n", host, port, err)
		return nil
	}
	if _, err := client.ListCollections(connCtx); err != nil {
		fmt.Fprintf(os.Stderr, "[warn] qdrant list collections failed (%v) — vector sweep will be skipped\n", err)
		_ = client.Close()
		return nil
	}
	return client
}

// qdMatchingCollections returns every collection weknora owns (prefix match
// on the configured base name; per-dimension siblings look like
// "<base>_<dim>").
func qdMatchingCollections(ctx context.Context, client *qdrant.Client) []string {
	base := envOr("QDRANT_COLLECTION", "weknora_embeddings")
	collections, err := client.ListCollections(ctx)
	if err != nil {
		logger.Warnf(ctx, "[admin-cleanup-orphans] qdrant list collections: %v", err)
		return nil
	}
	out := make([]string, 0, len(collections))
	for _, c := range collections {
		if c == base || strings.HasPrefix(c, base+"_") {
			out = append(out, c)
		}
	}
	return out
}

// qdCountByKB counts points in `collection` whose payload has
// knowledge_base_id == kbID. Uses the index-backed filter for parity with
// the production delete path.
func qdCountByKB(ctx context.Context, client *qdrant.Client, collection, kbID string) (int, error) {
	filter := &qdrant.Filter{
		Must: []*qdrant.Condition{qdrant.NewMatchKeywords("knowledge_base_id", kbID)},
	}
	n, err := client.Count(ctx, &qdrant.CountPoints{
		CollectionName: collection,
		Filter:         filter,
		Exact:          boolPtr(true),
	})
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func qdCountAll(ctx context.Context, client *qdrant.Client, collection string) (int64, error) {
	n, err := client.Count(ctx, &qdrant.CountPoints{
		CollectionName: collection,
		Exact:          boolPtr(true),
	})
	if err != nil {
		return 0, err
	}
	return int64(n), nil
}

// qdDeleteByKB removes every point in `collection` whose payload has
// knowledge_base_id == kbID. Returns the number actually removed.
func qdDeleteByKB(ctx context.Context, client *qdrant.Client, collection, kbID string) (int, error) {
	filter := &qdrant.Filter{
		Must: []*qdrant.Condition{qdrant.NewMatchKeywords("knowledge_base_id", kbID)},
	}
	before, err := client.Count(ctx, &qdrant.CountPoints{
		CollectionName: collection,
		Filter:         filter,
		Exact:          boolPtr(true),
	})
	if err != nil {
		return 0, fmt.Errorf("count before: %w", err)
	}
	if before == 0 {
		return 0, nil
	}
	if _, err := client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: collection,
		Points:         qdrant.NewPointsSelectorFilter(filter),
	}); err != nil {
		return 0, err
	}
	// Qdrant's delete-by-filter is async; the payload index removal is fast
	// but the segment cleanup can lag. We re-count up to a short window so
	// the operator gets a non-zero "deleted" number whenever the collection
	// actually held the points.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		after, err := client.Count(ctx, &qdrant.CountPoints{
			CollectionName: collection,
			Filter:         filter,
			Exact:          boolPtr(true),
		})
		if err == nil && after == 0 {
			return int(before), nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return int(before), nil
}

// qdFlushCollection drops every point from the named collection. Irreversible;
// only invoked when -flush-qdrant is set. Implemented via an empty filter
// because the qdrant v1.18.1 client has no "delete all" selector.
func qdFlushCollection(ctx context.Context, client *qdrant.Client, collection string) (int64, error) {
	before, err := client.Count(ctx, &qdrant.CountPoints{
		CollectionName: collection,
		Exact:          boolPtr(true),
	})
	if err != nil {
		return 0, fmt.Errorf("count before flush: %w", err)
	}
	if before == 0 {
		return 0, nil
	}
	// Empty filter matches every point — the canonical "delete all" pattern
	// in Qdrant when the client has no dedicated all-selector.
	emptyFilter := &qdrant.Filter{}
	if _, err := client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: collection,
		Points:         qdrant.NewPointsSelectorFilter(emptyFilter),
	}); err != nil {
		return 0, err
	}
	// Same async-window trick as qdDeleteByKB.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		after, err := client.Count(ctx, &qdrant.CountPoints{
			CollectionName: collection,
			Exact:          boolPtr(true),
		})
		if err == nil && after == 0 {
			return int64(before), nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return int64(before), nil
}

func boolPtr(b bool) *bool { return &b }

func isQueueNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	return err == asynq.ErrQueueNotFound || strings.Contains(err.Error(), "queue not found")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[fatal] "+format+"\n", args...)
	os.Exit(1)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func filterDescription(all bool, days int) string {
	if all {
		return "all soft-deleted KBs"
	}
	return fmt.Sprintf("soft-deleted KBs within last %d days", days)
}

func asynqSweepDescription(insp *asynq.Inspector) string {
	if insp == nil {
		return "SKIPPED (no REDIS reachable)"
	}
	return "all queues × {pending, scheduled, retry, active, archived, completed}"
}

func qdrantSweepDescription() string {
	if openQdrant() == nil {
		return "SKIPPED (no QDRANT_HOST reachable)"
	}
	return "per-KB filter delete (use -flush-qdrant to wipe the whole collection)"
}
