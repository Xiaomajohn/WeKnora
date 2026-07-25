-- Migration: 000069_users_user_role
-- Description: Add a global user-level role column ('normal' / 'admin')
-- orthogonal to per-tenant TenantRole (owner/admin/contributor/viewer).
-- Controls whether the SPA exposes any Settings entry point. The SPA is
-- already authorised via IsSystemAdmin / CanAccessAllTenants, so this
-- column is intentionally write-read by both backend (for the API) and
-- frontend (for the gate). Default 'normal' preserves existing behaviour
-- for legacy users and self-registration. Must NOT be writable on
-- accounts where IsSystemAdmin=true.

DO $$ BEGIN RAISE NOTICE '[Migration 000069] Adding users.user_role...'; END $$;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS user_role VARCHAR(20) NOT NULL DEFAULT 'normal';

CREATE INDEX IF NOT EXISTS idx_users_user_role ON users(user_role);

DO $$ BEGIN RAISE NOTICE '[Migration 000069] users.user_role ready'; END $$;
