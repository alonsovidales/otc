-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- When a device was last connected to the bridge (devices.last_online_at):
-- stamped when it connects to a node, every 10 minutes while it stays
-- connected and when its last connection there closes. A device in use
-- only at home or over Tailscale never has a client relayed through the
-- bridge (last_client_at), but it stays connected: the inactivity job
-- (issue #176) counts this as use, so such an account is never warned or
-- removed while its device is on. Also the admin panel's "Last online"
-- and the account's data export. Idempotent: run it on the bridge's
-- database once (`sudo mysql otc < 012-last-online.sql`, on the primary),
-- re-running is harmless. Best before deploying the bridge that uses it -
-- that bridge goes on without the column, but stamps nothing until it
-- exists and counts only sign-ins and relayed clients as use, as before.
DROP PROCEDURE IF EXISTS otc_last_online_migrate;
DELIMITER //
CREATE PROCEDURE otc_last_online_migrate()
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'devices' AND column_name = 'last_online_at') THEN
    ALTER TABLE devices ADD COLUMN `last_online_at` datetime DEFAULT NULL;
  END IF;
END //
DELIMITER ;
CALL otc_last_online_migrate();
DROP PROCEDURE otc_last_online_migrate;
