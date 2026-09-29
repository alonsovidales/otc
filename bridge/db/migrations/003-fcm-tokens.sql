-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Issue #125: Android push through Firebase Cloud Messaging - the tokens
-- each device reports for its Android phones. Idempotent: run it on the
-- bridge's database once (`sudo mysql otc < 003-fcm-tokens.sql`),
-- re-running is harmless. Before deploying the bridge that uses it.

CREATE TABLE IF NOT EXISTS push_fcm_tokens
(
  `domain` varchar(150) not null,
  `token`  varchar(512) not null,

  key (`domain`)
) engine=InnoDB;
