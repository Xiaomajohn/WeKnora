-- Migration 000068 rollback.
-- Reverses 000068_users_registered_via_invite by dropping the
-- registered_via_invite column from users.

DO $$ BEGIN RAISE NOTICE '[Migration 000068] Dropping users.registered_via_invite...'; END $$;

ALTER TABLE users
    DROP COLUMN IF EXISTS registered_via_invite;

DO $$ BEGIN RAISE NOTICE '[Migration 000068] users.registered_via_invite dropped'; END $$;
