-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Device relay secrets and setup codes kept hashed, step 1 of 2: a read of
-- the database (a backup, the replica's host, a dump) should not hand out
-- what registers as every device, nor live setup codes.
--
-- - devices.secret_hash: the SHA-256 (hex) of the device's relay secret,
--   filled in here for every device from devices.secret.
-- - account_setup_codes: setup codes by a keyed hash of the code (the key
--   is the bridge's, never in the database), and whether a code may move one
--   of the account's names to a new device (made right after its owner
--   signed in, not for a session alone).
--
-- The plaintext stays for now: a bridge node of an older release reads only
-- devices.secret and account_tokens, and the cluster is deployed one node at
-- a time. A bridge from this release writes both forms while both exist.
-- Step 2, 014-drop-plaintext-secrets.sql, drops the plaintext once every
-- node runs it (bridge/cluster/README.md, "Hashed secrets").
--
-- Idempotent: run it on the bridge's database once
-- (`sudo mysql otc < 013-hashed-secrets.sql`, on the primary), re-running is
-- harmless and fills secret_hash in again. Either side of deploying the
-- bridge that uses it: that bridge goes on without these, as before.

SET @s = (SELECT IF(COUNT(*) = 0,
  'ALTER TABLE devices ADD COLUMN `secret_hash` char(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL',
  'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'devices' AND column_name = 'secret_hash');
PREPARE st FROM @s; EXECUTE st; DEALLOCATE PREPARE st;

-- The secret's bytes as UTF-8, as the bridge hashes the secret a device
-- presents. Nothing to fill in once 014 has dropped the plaintext.
SET @s = (SELECT IF(COUNT(*) = 1,
  'UPDATE devices SET secret_hash = SHA2(CONVERT(secret USING utf8mb4), 256) WHERE CHAR_LENGTH(secret) > 0',
  'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'devices' AND column_name = 'secret');
PREPARE st FROM @s; EXECUTE st; DEALLOCATE PREPARE st;

CREATE TABLE IF NOT EXISTS account_setup_codes (
  -- HMAC-SHA256 of the code, hex.
  `code_hash` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `account_id` varchar(36) NOT NULL,
  -- Made right after its owner proved who they are (a password, a Google
  -- or Apple sign-in): only such a code moves an account's name to a new
  -- device.
  `owner_confirmed` tinyint(1) NOT NULL DEFAULT 0,
  `created` datetime NOT NULL,
  `expires` datetime NOT NULL,
  PRIMARY KEY (`code_hash`),
  KEY (`account_id`),
  KEY (`expires`)
) ENGINE=InnoDB;
