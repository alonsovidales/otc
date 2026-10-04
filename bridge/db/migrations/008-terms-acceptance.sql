-- Issue #175: which version of the terms of use (/terms) an account
-- accepted, and when. Accounts from before stay NULL until their owner
-- accepts them on the account page. Idempotent.
DROP PROCEDURE IF EXISTS otc_terms_migrate;
DELIMITER //
CREATE PROCEDURE otc_terms_migrate()
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'accounts' AND column_name = 'terms_version') THEN
    ALTER TABLE accounts ADD COLUMN `terms_version` varchar(16) null, ADD COLUMN `terms_accepted_at` datetime null;
  END IF;
END //
DELIMITER ;
CALL otc_terms_migrate();
DROP PROCEDURE otc_terms_migrate;
