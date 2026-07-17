-- Migration: 000068_users_registered_via_invite
-- Description: Flag accounts created via the share-link
-- /auth/register-by-invite flow (TenantProvisioningTenantless) so the
-- SPA can hide privileged settings entries (members / models / "all
-- settings") for these accounts. UI-only gate; no API or route
-- behaviour depends on this column.

DO $$ BEGIN RAISE NOTICE '[Migration 000068] Adding users.registered_via_invite...'; END $$;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS registered_via_invite BOOLEAN NOT NULL DEFAULT FALSE;

DO $$ BEGIN RAISE NOTICE '[Migration 000068] users.registered_via_invite ready'; END $$;
