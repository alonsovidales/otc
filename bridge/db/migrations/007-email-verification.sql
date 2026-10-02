-- Email verification and password reset (accounts must verify their email
-- before they can register a device name). Accounts that exist already are
-- kept as verified. Idempotent.
DROP PROCEDURE IF EXISTS otc_email_verify_migrate;
DELIMITER //
CREATE PROCEDURE otc_email_verify_migrate()
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'accounts' AND column_name = 'email_verified') THEN
    ALTER TABLE accounts ADD COLUMN `email_verified` tinyint(1) NOT NULL DEFAULT 0;
    UPDATE accounts SET email_verified = 1;
  END IF;
END //
DELIMITER ;
CALL otc_email_verify_migrate();
DROP PROCEDURE otc_email_verify_migrate;

CREATE TABLE IF NOT EXISTS account_email_tokens (
  `token_hash` char(64) not null,
  `account_id` varchar(36) not null,
  `purpose` varchar(16) not null,
  `created` datetime not null,
  `expires` datetime not null,
  primary key (`token_hash`),
  key (`account_id`),
  key (`expires`)
) engine=InnoDB;
