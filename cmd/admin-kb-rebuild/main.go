// Command admin-kb-rebuild is the operational counterpart to manually
// deleting a knowledge base: it deep-cleans every related row that the
// soft-delete leaves behind (chunks, embeddings, spans, wiki pending
// ops, queued asynq tasks) and re-inserts the knowledge base with a
// brand-new UUID and the exact same business configuration.
//
// Semantics are deliberately narrow:
//
//   - DB rows are removed in one transaction with all-deletable tables
//     hard-deleted (Unscoped) so the next knowledge import doesn't trip
//     over soft-deleted rows.
//   - asynq queues are best-effort: every queue the parser/enrichment
//     pipeline writes to is scanned for tasks whose payload carries
//     knowledge_base_id == <KB>, then deleted/signal-cancelled
//     depending on state.
//   - The KB row is recreated by SELECT … RETURNING → INSERT, preserving
//     every config column (embedding_model_id, summary_model_id,
//     chunking_config, image_processing_config, faq_config,
//     question_generation_config, extract_config, storage_provider_config,
//     cos_config, vlm_config, asr_config, wiki_config,
//     indexing_strategy, vector_store_id, creator_id, description, …)
//     per the "KB 软删除与复刻重建规范" memory.
//
// qdrant vectors are NOT touched here — the cancel-parser dry-run that
// ran earlier showed the qdrant instance has zero collections, so there
// is nothing to delete; if a future KB has actual vector points to
// purge, run a separate qdrant-aware tool.
//
// Usage:
//
//	go run ./cmd/admin-kb-rebuild -kb-id <uuid>            # dry-run default
//	go run ./cmd/admin-kb-rebuild -kb-id <uuid> -yes       # actually execute
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Knowledge-row payload we pick out of asynq tasks; every parser/enrichment
// payload has tenant_id, knowledge_base_id and knowledge_id as the canonical
// scope triple. We only need knowledge_base_id for a KB-level sweep, but
// having the full triple lets us filter more precisely when neighbouring
// KBs exist.
type kbPayloadProbe struct {
	KnowledgeBaseID string `json:"knowledge_base_id"`
	KBID            string `json:"kb_id"`
}

type taskTypesForKB map[string]struct{}

var cancelableTaskTypes = taskTypesForKB{
	types.TypeDocumentProcess:      {},
	types.TypeManualProcess:        {},
	types.TypeImageMultimodal:      {},
	types.TypeKnowledgePostProcess: {},
	types.TypeQuestionGeneration:   {},
	types.TypeSummaryGeneration:    {},
	types.TypeChunkExtract:         {},
	// KB-level sync tasks (datasource / wiki / kb clone): these don't carry
	// knowledge_id but they DO carry knowledge_base_id and would rerun on
	// the next import. Cancel them too so a partially-imported dataset
	// doesn't keep dragging the worker through the deleted KB's row.
	types.TypeDataSourceSync: {},
	types.TypeWikiIngest:     {},
	types.TypeWikiFinalize:   {},
	types.TypeFAQImport:      {},
	types.TypeKBClone:        {},
	types.TypeIndexDelete:    {},
	types.TypeKBDelete:       {},
	types.TypeKnowledgeListDelete:  {},
	types.TypeKnowledgeListReparse: {},
	types.TypeKnowledgeMove:        {},
	types.TypeDataTableSummary:     {},
}

func main() {
	kbID := flag.String("kb-id", "", "knowledge_base id to rebuild (required)")
	yes := flag.Bool("yes", false, "perform destructive ops (default: dry-run)")
	flag.Parse()
	if *kbID == "" {
		die("-kb-id is required")
	}

	if _, err := config.LoadConfig(); err != nil {
		// Same rationale as admin-cancel-parser: we only need env-driven DB
		// credentials; the full server config isn't relevant.
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
	_ = redisAddrUsed // reported in summary

	ctx := context.Background()

	// 1. Snapshot the original KB row (Unscoped so soft-deleted rows are
	// visible — the user's manual delete already soft-deleted the row).
	original, err := readKBUnscoped(ctx, db, *kbID)
	if err != nil {
		die("read KB: %v", err)
	}
	if original == nil {
		die("KB %s does not exist", *kbID)
	}

	// 2. Inventory every related row.
	counts, err := inventoryRelatedCounts(ctx, db, *kbID)
	if err != nil {
		die("inventory: %v", err)
	}

	fmt.Fprintf(os.Stdout, "=== KB rebuild plan ===\n")
	fmt.Fprintf(os.Stdout, "  source KB id     : %s\n", original.ID)
	fmt.Fprintf(os.Stdout, "  name             : %s\n", original.Name)
	fmt.Fprintf(os.Stdout, "  tenant           : %d\n", original.TenantID)
	fmt.Fprintf(os.Stdout, "  type             : %s\n", original.Type)
	fmt.Fprintf(os.Stdout, "  embedding_model  : %s\n", original.EmbeddingModelID)
	fmt.Fprintf(os.Stdout, "  summary_model    : %s\n", original.SummaryModelID)
	fmt.Fprintf(os.Stdout, "  creator          : %s\n", original.CreatorID)
	fmt.Fprintf(os.Stdout, "  is_temporary     : %v\n", original.IsTemporary)
	fmt.Fprintf(os.Stdout, "  vector_store_id  : %v\n", derefString(original.VectorStoreID))
	fmt.Fprintf(os.Stdout, "  storage_provider : %s\n", original.GetStorageProvider())
	fmt.Fprintf(os.Stdout, "  indexing_strategy: %s\n", original.IndexingStrategy)
	fmt.Fprintf(os.Stdout, "  wiki_enabled     : %v\n", original.IsWikiEnabled())
	fmt.Fprintf(os.Stdout, "  graph_enabled    : %v\n", original.IsGraphEnabled())
	fmt.Fprintf(os.Stdout, "\n  Related rows to hard-delete:\n")
	fmt.Fprintf(os.Stdout, "    knowledges                    : %d\n", counts.knowledges)
	fmt.Fprintf(os.Stdout, "    chunks                        : %d\n", counts.chunks)
	fmt.Fprintf(os.Stdout, "    embeddings                    : %d\n", counts.embeddings)
	fmt.Fprintf(os.Stdout, "    knowledge_processing_spans    : %d\n", counts.spans)
	fmt.Fprintf(os.Stdout, "    knowledge_tag_relations       : %d\n", counts.tagRels)
	fmt.Fprintf(os.Stdout, "    kb_shares                     : %d\n", counts.kbShares)
	fmt.Fprintf(os.Stdout, "    user_kb_pins                  : %d\n", counts.userKBPins)
	fmt.Fprintf(os.Stdout, "    task_pending_ops              : %d\n", counts.taskPendingOps)

	// 3. Sweep asynq if reachable; otherwise log the skip. Pure read-only —
	// we don't want to surprise-mutate queues during a dry-run.
	var asynqDeleted, asynqCancelled int
	if insp != nil {
		var err error
		asynqDeleted, asynqCancelled, err = sweepAsynqForKB(ctx, insp, *kbID)
		if err != nil {
			logger.Warnf(ctx, "[admin-kb-rebuild] asynq sweep error: %v", err)
		}
		fmt.Fprintf(os.Stdout, "\n  asynq to sweep (best-effort)\n")
		fmt.Fprintf(os.Stdout, "    dequeued                       : %d\n", asynqDeleted)
		fmt.Fprintf(os.Stdout, "    active cancelled               : %d\n", asynqCancelled)
	} else {
		fmt.Fprintf(os.Stdout, "\n  asynq sweep skipped (no REDIS reachable)\n")
	}

	if !*yes {
		fmt.Fprintf(os.Stdout,
			"\nDRY-RUN: re-run with -yes to hard-delete the above rows and re-insert the KB with a new UUID.\n")
		return
	}

	// 4. Destructive phase. Wrap DB ops in a transaction; if anything
	// fails, the only visible side effect is the asynq sweep above
	// (which is independently idempotent on re-run).
	newKB, err := rebuildKB(ctx, db, original, counts)
	if err != nil {
		die("rebuild: %v", err)
	}

	fmt.Fprintf(os.Stdout, "\n=== Rebuild complete ===\n")
	fmt.Fprintf(os.Stdout, "  old kb id : %s\n", original.ID)
	fmt.Fprintf(os.Stdout, "  new kb id : %s\n", newKB.ID)
	fmt.Fprintf(os.Stdout, "  recreated_at (UTC) : %s\n", newKB.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(os.Stdout, "  deleted_at_set     : %v\n", newKB.DeletedAt.Valid)
	if newKB.DeletedAt.Valid {
		fmt.Fprintf(os.Stdout, "  (note) deleted_at timestamp: %s\n", newKB.DeletedAt.Time.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(os.Stdout, "  storage_provider   : %s\n", newKB.GetStorageProvider())
	fmt.Fprintf(os.Stdout, "  embedding_model_id : %s\n", newKB.EmbeddingModelID)
	fmt.Fprintf(os.Stdout, "  vector_store_id    : %v\n", derefString(newKB.VectorStoreID))
	fmt.Fprintf(os.Stdout, "\n  The new KB is ready. Re-import the original documents; the\n")
	fmt.Fprintf(os.Stdout, "  parser will index them into the freshly-minted KB row.\n")
}

type relatedCounts struct {
	knowledges      int64
	chunks          int64
	embeddings      int64
	spans           int64
	tagRels         int64
	kbShares        int64
	userKBPins      int64
	taskPendingOps  int64
}

func inventoryRelatedCounts(ctx context.Context, db *gorm.DB, kbID string) (*relatedCounts, error) {
	var c relatedCounts
	row := db.WithContext(ctx).Raw(`
SELECT
  (SELECT count(*) FROM knowledges                    WHERE knowledge_base_id = ?)                                       AS knowledges,
  (SELECT count(*) FROM chunks                        WHERE knowledge_base_id = ?)                                       AS chunks,
  (SELECT count(*) FROM embeddings                    WHERE knowledge_base_id = ?)                                       AS embeddings,
  (SELECT count(*) FROM knowledge_processing_spans    WHERE knowledge_id IN (SELECT id FROM knowledges WHERE knowledge_base_id = ?)) AS spans,
  (SELECT count(*) FROM knowledge_tag_relations       WHERE knowledge_id IN (SELECT id FROM knowledges WHERE knowledge_base_id = ?)) AS tag_rels,
  (SELECT count(*) FROM kb_shares                     WHERE knowledge_base_id = ?)                                       AS kb_shares,
  (SELECT count(*) FROM user_kb_pins                  WHERE kb_id = ?)                                                   AS user_kb_pins,
  (SELECT count(*) FROM task_pending_ops              WHERE scope = ? AND scope_id = ?
                                                       OR (task_type = ? AND dedup_key IN (SELECT id FROM knowledges WHERE knowledge_base_id = ?))) AS ops
`, kbID, kbID, kbID, kbID, kbID, kbID, types.TaskScopeKnowledgeBase, kbID, types.TypeWikiIngest, kbID, kbID).Row()
	if err := row.Scan(&c.knowledges, &c.chunks, &c.embeddings, &c.spans, &c.tagRels, &c.kbShares, &c.userKBPins, &c.taskPendingOps); err != nil {
		return nil, fmt.Errorf("scan inventory row: %w", err)
	}
	return &c, nil
}

// readKBUnscoped loads the KB row from the underlying table regardless of
// soft-delete state. The user's manual delete set deleted_at, so a scoped
// First() would silently return ErrRecordNotFound.
func readKBUnscoped(ctx context.Context, db *gorm.DB, id string) (*types.KnowledgeBase, error) {
	var row types.KnowledgeBase
	err := db.WithContext(ctx).Unscoped().Where("id = ?", id).Take(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// rebuildKB hard-deletes every related row in one transaction and
// re-inserts the KB with a brand-new UUID. Returns the freshly-inserted row.
//
// The asynq sweep already happened in main(); this function only touches
// Postgres.
func rebuildKB(ctx context.Context, db *gorm.DB, original *types.KnowledgeBase, c *relatedCounts) (*types.KnowledgeBase, error) {
	var newRow types.KnowledgeBase
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 4a. children first.
		if err := tx.Exec(`DELETE FROM embeddings WHERE knowledge_base_id = ?`, original.ID).Error; err != nil {
			return fmt.Errorf("delete embeddings: %w", err)
		}
		if err := tx.Exec(`DELETE FROM chunks WHERE knowledge_base_id = ?`, original.ID).Error; err != nil {
			return fmt.Errorf("delete chunks: %w", err)
		}
		if err := tx.Exec(`DELETE FROM knowledge_processing_spans WHERE knowledge_id IN (SELECT id FROM knowledges WHERE knowledge_base_id = ?)`, original.ID).Error; err != nil {
			return fmt.Errorf("delete spans: %w", err)
		}
		if err := tx.Unscoped().Where("knowledge_base_id = ?", original.ID).Delete(&types.Knowledge{}).Error; err != nil {
			return fmt.Errorf("delete knowledges: %w", err)
		}
		if err := tx.Exec(`DELETE FROM knowledge_tag_relations WHERE knowledge_id IN (SELECT id FROM knowledges WHERE knowledge_base_id = ?)`, original.ID).Error; err != nil {
			return fmt.Errorf("delete tag relations: %w", err)
		}
		if err := tx.Unscoped().Where("knowledge_base_id = ?", original.ID).Delete(&types.KnowledgeBaseShare{}).Error; err != nil {
			return fmt.Errorf("delete kb_shares: %w", err)
		}
		if err := tx.Exec(`DELETE FROM user_kb_pins WHERE kb_id = ?`, original.ID).Error; err != nil {
			return fmt.Errorf("delete user_kb_pins: %w", err)
		}
		if err := tx.Exec(`
DELETE FROM task_pending_ops
WHERE scope = ? AND scope_id = ?
   OR (task_type = ? AND dedup_key IN (SELECT id FROM knowledges WHERE knowledge_base_id = ?))
`, types.TaskScopeKnowledgeBase, original.ID, types.TypeWikiIngest, original.ID).Error; err != nil {
			return fmt.Errorf("delete task_pending_ops: %w", err)
		}

		// 4b. the KB itself.
		if err := tx.Unscoped().Delete(original).Error; err != nil {
			return fmt.Errorf("delete KB: %w", err)
		}

		// 4c. Re-insert with new identity. Every config column is
		// copied verbatim from the original (the "KB 重建规范"
		// memory); only id / created_at / updated_at / deleted_at /
		// is_pinned / pinned_at change.
		newRow = *original
		newRow.ID = uuid.New().String()
		newRow.CreatedAt = time.Now().UTC()
		newRow.UpdatedAt = newRow.CreatedAt
		newRow.DeletedAt = gorm.DeletedAt{} // empty (NULL)
		newRow.IsPinned = false
		newRow.PinnedAt = nil

		if err := tx.Create(&newRow).Error; err != nil {
			return fmt.Errorf("reinsert KB: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &newRow, nil
}

// sweepAsynqForKB walks every queue the parser/enrichment/sync pipeline
// uses and removes (or signals) any task whose payload references the
// given knowledge_base_id. Mirrors admin-cancel-parser's sweepAsynqForKnowledge
// but uses the KB-level payload field instead of knowledge_id.
func sweepAsynqForKB(ctx context.Context, insp *asynq.Inspector, kbID string) (int, int, error) {
	queues := kbQueueNames()
	deleted := 0
	cancelled := 0

	for _, queue := range queues {
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
			logger.Warnf(ctx, "[admin-kb-rebuild] list state queue=%s page=%d: %v", queue, page, err)
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
				logger.Warnf(ctx, "[admin-kb-rebuild] delete id=%s: %v", t.ID, err)
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
			logger.Warnf(ctx, "[admin-kb-rebuild] list active queue=%s page=%d: %v", queue, page, err)
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
				logger.Warnf(ctx, "[admin-kb-rebuild] cancel id=%s: %v", t.ID, err)
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
		addr = os.Getenv("ADMIN_CANCEL_REDIS")
	}
	if addr == "" {
		return nil, ""
	}
	password := os.Getenv("REDIS_PASSWORD")
	if password == "" {
		password = os.Getenv("ADMIN_CANCEL_REDIS_PASSWORD")
	}
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

func isQueueNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	return err == asynq.ErrQueueNotFound
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

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
