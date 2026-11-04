-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Device relay secrets and setup codes kept hashed, step 2 of 2: the
-- plaintext goes. devices.secret_hash is filled in once more from
-- devices.secret (a node of an older release may have written a secret
-- since step 1), then devices.secret and account_tokens are dropped. From
-- here on the bridge keeps a device's secret and the setup codes only as
-- hashes (013-hashed-secrets.sql).
--
-- Only once every bridge node runs a release that reads the hashes, and 15
-- minutes after the last of them restarted - a setup code an older node made
-- lives that long, in account_tokens - (bridge/cluster/README.md, "Hashed
-- secrets"). A node of an older release can't check any device after this:
-- going back to one means restoring the column from a backup.
--
-- It does step 1's work first, so it is safe on a database where 013 never
-- ran. Idempotent: `sudo mysql otc < 014-drop-plaintext-secrets.sql`, on the
-- primary; re-running is harmless.

SET @s = (SELECT IF(COUNT(*) = 0,
  'ALTER TABLE devices ADD COLUMN `secret_hash` char(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL',
  'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'devices' AND column_name = 'secret_hash');
PREPARE st FROM @s; EXECUTE st; DEALLOCATE PREPARE st;

SET @s = (SELECT IF(COUNT(*) = 1,
  'UPDATE devices SET secret_hash = SHA2(CONVERT(secret USING utf8mb4), 256) WHERE CHAR_LENGTH(secret) > 0',
  'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'devices' AND column_name = 'secret');
PREPARE st FROM @s; EXECUTE st; DEALLOCATE PREPARE st;

SET @s = (SELECT IF(COUNT(*) = 1,
  'ALTER TABLE devices DROP COLUMN `secret`',
  'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'devices' AND column_name = 'secret');
PREPARE st FROM @s; EXECUTE st; DEALLOCATE PREPARE st;

CREATE TABLE IF NOT EXISTS account_setup_codes (
  `code_hash` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `account_id` varchar(36) NOT NULL,
  `owner_confirmed` tinyint(1) NOT NULL DEFAULT 0,
  `created` datetime NOT NULL,
  `expires` datetime NOT NULL,
  PRIMARY KEY (`code_hash`),
  KEY (`account_id`),
  KEY (`expires`)
) ENGINE=InnoDB;

DROP TABLE IF EXISTS account_tokens;
