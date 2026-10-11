-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Localization (docs/i18n.md): the language the bridge writes in.
-- accounts.lang is the language of the account's emails - set at sign-up
-- from the page or the browser, changed only by the signed-in owner.
-- push_registrations.language is the language of the bridge's own pushes
-- to a device's phones and browsers (the device-offline alert), as the
-- device reports it in UpdatePushRegistrations. '' in either = not known
-- (English). Idempotent: run it on the bridge's database once
-- (`sudo mysql otc < 010-language.sql`), re-running is harmless. Before
-- deploying the bridge that uses it - that bridge goes on without the
-- columns, but keeps no language until they exist.
DROP PROCEDURE IF EXISTS otc_language_migrate;
DELIMITER //
CREATE PROCEDURE otc_language_migrate()
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'accounts' AND column_name = 'lang') THEN
    ALTER TABLE accounts ADD COLUMN `lang` varchar(16) NOT NULL DEFAULT '';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'push_registrations' AND column_name = 'language') THEN
    ALTER TABLE push_registrations ADD COLUMN `language` varchar(16) NOT NULL DEFAULT '';
  END IF;
END //
DELIMITER ;
CALL otc_language_migrate();
DROP PROCEDURE otc_language_migrate;
