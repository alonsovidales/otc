-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Security advisory (released names): a released device name can't be
-- taken by another account for 30 days - a new holder used to inherit the
-- previous device's friendships. Idempotent: `sudo mysql otc <
-- 004-released-domains.sql`, before deploying the bridge that uses it.

CREATE TABLE IF NOT EXISTS released_domains
(
  `domain`      varchar(150) not null,
  `account_id`  varchar(64) default null,
  `released_at` datetime not null,

  primary key (`domain`)
) engine=InnoDB;
