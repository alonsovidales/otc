-- Issue #176: when an inactive account was warned that it will be removed
-- (accounts.InactivityJob). Cleared when the account is used again.
-- Idempotent.
DROP PROCEDURE IF EXISTS otc_inactivity_migrate;
DELIMITER //
CREATE PROCEDURE otc_inactivity_migrate()
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'accounts' AND column_name = 'inactivity_warned_at') THEN
    ALTER TABLE accounts ADD COLUMN `inactivity_warned_at` datetime null;
  END IF;
END //
DELIMITER ;
CALL otc_inactivity_migrate();
DROP PROCEDURE otc_inactivity_migrate;
