-- Migration 000138_localize_builtin_agent_prompts
-- Description: 本地化内置智能体提示词为中文。
--
-- 历史背景：
--   * migrations/versioned/000006_custom_agents.up.sql 在每个 tenant 下
--     插入了 builtin-quick-answer 和 builtin-smart-reasoning 行，
--     并把 tenant.conversation_config.prompt /
--     tenant.agent_config.system_prompt_web_disabled 的英文默认值拷贝到了
--     custom_agents.config.system_prompt 字段。
--   * builtin-data-analyst / builtin-wiki-researcher 没有专门的迁移，
--     但如果用户在 UI 上编辑过它们的 config，同样会写入对应行。
--   * 这些行的 system_prompt 字段是英文模板，与现在
--     config/prompt_templates/agent_system_prompt.yaml 中的中文模板不一致。
--
-- 策略：
--   * 删除 4 个内置智能体 config 中残留的 system_prompt /
--     system_prompt_web_enabled 字段，让 customAgentService.ListAgents /
--     GetAgentByID 在首次读取时检测到这些字段为空，从最新的
--     builtin_agents.yaml + prompt_templates/ 中文模板重新填充并回写。
--   * 仅当 is_builtin = true 时清空，避免误改用户自己创建的同名 agent。
--   * 这里使用 jsonb 的 `key -` 操作（仅 Postgres 语法）。SQLite 不走本迁移
--     —— sqlite init 不插入内置 agent 行，运行时直接 fallback 到
--     builtin_agents.yaml 的最新内容；MySQL 当前未启用 versioned 迁移。

DO $$ BEGIN RAISE NOTICE '[Migration 000138] Localizing builtin agent prompts...'; END $$;

UPDATE custom_agents
SET config = config - 'system_prompt' - 'system_prompt_web_enabled',
    updated_at = NOW()
WHERE id IN (
    'builtin-quick-answer',
    'builtin-smart-reasoning',
    'builtin-data-analyst',
    'builtin-wiki-researcher'
)
  AND is_builtin = true;