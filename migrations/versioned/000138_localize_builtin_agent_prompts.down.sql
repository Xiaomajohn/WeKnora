-- Reverse 000138: cannot reliably reconstruct the original English prompt values,
-- so a downgrade keeps the field cleared (matching the post-upgrade state) and
-- relies on the YAML template fallback. This is intentional — the YAML is the
-- single source of truth for builtin prompts going forward.

DO $$ BEGIN RAISE NOTICE '[Migration 000138] Downgrade: leaving builtin agent system_prompt fields cleared.'; END $$;