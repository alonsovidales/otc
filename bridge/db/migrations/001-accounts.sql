-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Issue #124: user accounts on the bridge. Every domain registered from
-- now on belongs to an account (up to 5 per account); the account's owner
-- can see, release and re-issue them from https://<bridge>/account, and a
-- lost device is replaced by running setup again, signed in, with the
-- same name. Idempotent: run it on the bridge's database once
-- (`sudo mysql otc < 001-accounts.sql`), re-running is harmless.

CREATE TABLE IF NOT EXISTS accounts (
  `id` varchar(36) NOT NULL,
  `email` varchar(255) NOT NULL,
  `name` varchar(150) NOT NULL DEFAULT '',
  `surname` varchar(150) NOT NULL DEFAULT '',
  -- ISO 3166-1 alpha-2, the country of residence; empty until a provider
  -- sign-up completes its profile.
  `country` varchar(2) NOT NULL DEFAULT '',
  -- bcrypt; NULL for an account that only ever signed in with a provider.
  `password_hash` varchar(255) DEFAULT NULL,
  `created` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  -- The bridge is free for two years from sign-up (the terms shown at
  -- sign-up); billing comes later and reads this.
  `free_until` datetime NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY (`email`)
) ENGINE=InnoDB;

-- A sign-in provider's identity for an account: google/apple + the
-- provider's stable subject. One account can have several.
CREATE TABLE IF NOT EXISTS account_logins (
  `provider` varchar(16) NOT NULL,
  `subject` varchar(255) NOT NULL,
  `account_id` varchar(36) NOT NULL,
  PRIMARY KEY (`provider`, `subject`),
  KEY (`account_id`)
) ENGINE=InnoDB;

-- One-time, short-lived tokens: purpose "setup" is what the setup wizard
-- presents with /api/claim to say which account the new domain belongs to
-- (also typed by hand as a setup code).
CREATE TABLE IF NOT EXISTS account_tokens (
  `token` varchar(128) NOT NULL,
  `account_id` varchar(36) NOT NULL,
  `purpose` varchar(16) NOT NULL,
  `expires` datetime NOT NULL,
  PRIMARY KEY (`token`),
  KEY (`expires`)
) ENGINE=InnoDB;

-- OAuth "state" for an in-flight provider sign-in, with where to send the
-- browser afterwards.
CREATE TABLE IF NOT EXISTS oauth_states (
  `state` varchar(64) NOT NULL,
  `return_url` varchar(1024) NOT NULL DEFAULT '',
  `created` datetime NOT NULL,
  PRIMARY KEY (`state`),
  KEY (`created`)
) ENGINE=InnoDB;

ALTER TABLE devices ADD COLUMN IF NOT EXISTS `account_id` varchar(36) DEFAULT NULL;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS `created` datetime DEFAULT NULL;
ALTER TABLE devices ADD INDEX IF NOT EXISTS `account_id` (`account_id`);
