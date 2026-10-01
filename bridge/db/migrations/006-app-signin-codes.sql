-- Issue #144: app sign-in codes in the database, shared by every bridge
-- node, instead of one node's memory. Idempotent.
CREATE TABLE IF NOT EXISTS app_signin_codes (
  `code_hash` char(64) not null,
  `account_id` varchar(36) not null,
  `challenge` varchar(64) not null,
  `created` datetime not null,
  primary key (`code_hash`),
  key (`created`)
);
