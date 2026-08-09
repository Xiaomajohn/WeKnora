DROP INDEX IF EXISTS idx_knowledge_bases_scheduled_rebuild_enabled;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS scheduled_rebuild_config;