-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Issue #164: sessions that can be ended. An account's (and an admin's)
-- session cookie carries the epoch it was issued under; bumping it - a
-- password change, "sign out everywhere", an admin logout - ends every
-- session issued before. Idempotent (MySQL has no ADD COLUMN IF NOT
-- EXISTS, hence the check): `sudo mysql otc < 005-session-epoch.sql`,
-- before deploying the bridge that uses it.

SET @s = (SELECT IF(COUNT(*) = 0,
  'ALTER TABLE accounts ADD COLUMN session_epoch INT NOT NULL DEFAULT 0', 'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'accounts' AND column_name = 'session_epoch');
PREPARE st FROM @s; EXECUTE st; DEALLOCATE PREPARE st;

SET @s = (SELECT IF(COUNT(*) = 0,
  'ALTER TABLE admin_users ADD COLUMN session_epoch INT NOT NULL DEFAULT 0', 'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'admin_users' AND column_name = 'session_epoch');
PREPARE st FROM @s; EXECUTE st; DEALLOCATE PREPARE st;
