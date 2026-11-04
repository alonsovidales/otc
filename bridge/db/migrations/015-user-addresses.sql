-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Which device a user's address belongs to. A primary device registers the
-- addresses of its additional users (pepe.off-the.cloud) over the websocket
-- (ReqRegisterUserDomain), and gives one a new identity when its user is
-- added again. Only its own users' addresses may move that way: any device
-- of the account can send that request, and a name that moves takes every
-- client and friend that knows it along - the account's other names move
-- only with their owner's confirmation (a fresh sign-in, or the password).
--
-- - devices.primary_domain: the domain of the primary whose user's address
--   this is - set when the primary registers it, or asks for it again with
--   the address's own identity (at every start of the primary); NULL for
--   every other name. Cleared when the name moves to a new identity from the
--   account page or setup, and when the primary itself is released.
-- - released_domains.primary_domain: the same, kept while a released
--   address is held for its account, so that the user added again under the
--   same name gets it back.
--
-- Idempotent and independent of the other migrations' steps: run it on the
-- bridge's database once (`sudo mysql otc < 015-user-addresses.sql`, on the
-- primary); re-running is harmless. Best before deploying the bridge that
-- uses it: without these columns that bridge still registers new users'
-- addresses, but gives none a new identity over the websocket and gives
-- back none of the account's held names that way (the account page and
-- setup still do, with the owner's confirmation). A node of an older
-- release ignores them.

SET @s = (SELECT IF(COUNT(*) = 0,
  'ALTER TABLE devices ADD COLUMN `primary_domain` varchar(150) DEFAULT NULL',
  'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'devices' AND column_name = 'primary_domain');
PREPARE st FROM @s; EXECUTE st; DEALLOCATE PREPARE st;

-- Releasing a primary clears its users' references to it.
SET @s = (SELECT IF(COUNT(*) = 0,
  'ALTER TABLE devices ADD KEY `primary_domain` (`primary_domain`)',
  'SELECT 1')
  FROM information_schema.statistics
  WHERE table_schema = DATABASE() AND table_name = 'devices' AND index_name = 'primary_domain');
PREPARE st FROM @s; EXECUTE st; DEALLOCATE PREPARE st;

-- 004-released-domains.sql's table, for a database where it never ran.
CREATE TABLE IF NOT EXISTS released_domains
(
  `domain`      varchar(150) not null,
  `account_id`  varchar(64) default null,
  `released_at` datetime not null,

  primary key (`domain`)
) engine=InnoDB;

SET @s = (SELECT IF(COUNT(*) = 0,
  'ALTER TABLE released_domains ADD COLUMN `primary_domain` varchar(150) DEFAULT NULL',
  'SELECT 1')
  FROM information_schema.columns
  WHERE table_schema = DATABASE() AND table_name = 'released_domains' AND column_name = 'primary_domain');
PREPARE st FROM @s; EXECUTE st; DEALLOCATE PREPARE st;
