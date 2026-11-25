-- Reverse of the no-op placeholder. See the up migration for the rationale:
-- the original 000071 body already ran against every deployment that reached
-- version 71, so there is nothing to undo here.
DO $$ BEGIN RAISE NOTICE '[Migration 000071] Superseded by 000138; no-op.'; END $$;
