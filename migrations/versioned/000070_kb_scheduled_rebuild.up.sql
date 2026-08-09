-- Per-KB scheduled rebuild configuration.
--
-- Each KB may opt in to having its failed (or otherwise non-terminal)
-- documents periodically re-submitted through ReparseKnowledge. All
-- configuration knobs live on the row instead of in system_settings so
-- operators can configure each KB independently (e.g. interval,
-- batch size, eligible statuses).
--
-- Default schema:
--   {
--     "enabled":                    false,
--     "interval_seconds":           60,
--     "batch_size":                 1,
--     "statuses":                   ["failed"],
--     "mode":                       "sync",
--     "last_triggered_knowledge_id":   null,
--     "last_triggered_knowledge_name": null,
--     "last_triggered_at":             null
--   }
--
-- `mode` is "sync" | "async".
--   * sync — fire one rebuild, remember which document was triggered
--     (last_triggered_*). On the next tick the scheduler checks that
--     document's parse_status; if it is still in a non-terminal state
--     (pending / uploaded / processing / finalizing), the entire KB
--     is skipped for this tick. completed / failed / cancelled /
--     deleting all unblock the next trigger.
--   * async — fire batch_size rebuilds every interval regardless of
--     what previously-submitted documents are doing.
--
-- last_triggered_* snapshots the most recent rebuild trigger for the
-- KB editor's "最近触发的文档" panel. Name is denormalised on purpose
-- so the panel doesn't need a second API call.
--
-- NULL means "feature disabled for this KB" (no-op for the scheduler).
-- A non-NULL row with enabled=false is treated the same as NULL.
ALTER TABLE knowledge_bases
    ADD COLUMN IF NOT EXISTS scheduled_rebuild_config JSONB DEFAULT NULL;

COMMENT ON COLUMN knowledge_bases.scheduled_rebuild_config IS
    'Per-KB scheduled rebuild config. NULL = disabled. Otherwise: '
    '{"enabled":bool, "interval_seconds":int, "batch_size":int, '
    '"statuses":[string], "mode":"sync"|"async", '
    '"last_triggered_knowledge_id":string?, '
    '"last_triggered_knowledge_name":string?, '
    '"last_triggered_at":timestamp?}.';

-- GIN index so the scheduler can quickly find opted-in KBs across
-- tenants without scanning the whole table.
CREATE INDEX IF NOT EXISTS idx_knowledge_bases_scheduled_rebuild_enabled
    ON knowledge_bases ((scheduled_rebuild_config->>'enabled'))
    WHERE scheduled_rebuild_config IS NOT NULL
      AND deleted_at IS NULL;

DO $$ BEGIN RAISE NOTICE '[Migration 000070] scheduled_rebuild_config column added'; END $$;