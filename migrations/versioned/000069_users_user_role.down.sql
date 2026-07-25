-- Migration 000069 rollback.
-- Reverses 000069_users_user_role by dropping the user_role column
-- and its index from users. Safe to run; user_role is consumed only
-- by the SPA gate and the system admin protection in CreateUser /
-- UpdateUser / DeleteUser handlers.

DO $$ BEGIN RAISE NOTICE '[Migration 000069] Dropping users.user_role...'; END $$;

DROP INDEX IF EXISTS idx_users_user_role;

ALTER TABLE users
    DROP COLUMN IF EXISTS user_role;

DO $$ BEGIN RAISE NOTICE '[Migration 000069] users.user_role dropped'; END $$;
