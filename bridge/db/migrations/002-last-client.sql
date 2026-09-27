-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Issue #139: when a client last reached a device through the bridge, for
-- the admin panel's device list. Idempotent: run it on the bridge's
-- database once (`sudo mysql otc < 002-last-client.sql`), re-running is
-- harmless. MySQL (the production bridge) has no ADD COLUMN IF NOT EXISTS,
-- so a procedure checks first, as in 001-accounts.sql.

DROP PROCEDURE IF EXISTS otc_last_client_migrate;
DELIMITER //
CREATE PROCEDURE otc_last_client_migrate()
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'devices' AND column_name = 'last_client_at') THEN
    ALTER TABLE devices ADD COLUMN `last_client_at` datetime DEFAULT NULL;
  END IF;
END //
DELIMITER ;
CALL otc_last_client_migrate();
DROP PROCEDURE otc_last_client_migrate;
