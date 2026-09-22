CREATE USER 'otc'@'localhost' IDENTIFIED BY 'o2ufh2eiwKWmR3pe$wo';

drop database otc;
create database otc;

GRANT ALL PRIVILEGES ON otc.* TO 'otc'@'localhost';

use otc;

create table devices
(
  `owner_uuid` varchar(64) not null,
  `domain` varchar(150) not null,
  `secret` varchar(150) not null,
  -- Issue #93: set by the primary instance whenever it disables/re-enables
  -- one of its own additional users (issue #90) - that user's own process
  -- is actually stopped while disabled, so the bridge is the only place
  -- left that can tell a visitor *why* the domain suddenly can't be
  -- reached, rather than a generic connection failure.
  `disabled` tinyint(1) not null default 0,

  key (`owner_uuid`),
  unique (`domain`),
  key (`domain`)
) engine=InnoDB;

-- Bridge admin panel (issue #7): a single (or a few) operator accounts that
-- can log in to manage devices. password_hash is a bcrypt hash, never the
-- plaintext.
create table admin_users
(
  `username` varchar(64) not null,
  `password_hash` varchar(255) not null,
  `created` datetime not null,

  unique (`username`)
) engine=InnoDB;

-- Per-device metrics (issue #8): requests + bandwidth, aggregated into
-- hourly buckets so the panel can chart them without a row per message.
create table device_metrics
(
  `domain` varchar(150) not null,
  `hour_bucket` datetime not null,
  `requests` int not null default 0,
  `bytes_in` bigint not null default 0,
  `bytes_out` bigint not null default 0,

  unique key (`domain`, `hour_bucket`),
  key (`domain`)
) engine=InnoDB;

-- Messages submitted through the public site's contact form (issue #57):
-- a general "get in touch" or "give me bridge access" request. Reviewed
-- from the admin panel's Messages tab, no automated action taken on them.
create table contact_requests
(
  `id` int not null auto_increment,
  `name` varchar(150) not null,
  `email` varchar(255) not null,
  `reason` varchar(64) not null,
  `message` text not null,
  `created` datetime not null,
  `is_read` tinyint(1) not null default 0,

  primary key (`id`),
  key (`created`)
) engine=InnoDB;

-- Issue #38: the setup wizard's LAN-address hand-off. A device being set
-- up over its hotspot joins the owner's WiFi and loses the phone driving
-- the wizard; it reports its new LAN address here under a one-time token
-- the wizard page already holds, and the page polls for it. Rows are
-- worthless after ten minutes and purged on every write - this is a
-- hand-off, not a record, and lives in the database rather than in one
-- bridge process's memory so any bridge instance can answer the poll.
create table setup_beacons
(
  `token` varchar(128) not null,
  `addr` varchar(64) not null,
  `created` datetime not null,

  primary key (`token`),
  key (`created`)
) engine=InnoDB;

-- Per-device push-notification registrations, mirrored from the device
-- itself (issue #62: "alert the owner if their device goes unreachable" -
-- the bridge is the only party that can ever observe that, since offline
-- is a state the device itself can't report). vapid keys are one row per
-- domain; apns tokens and web push subscriptions are one row each, kept in
-- their own tables rather than a single delimited column so a domain can
-- have any number of either. All three are replaced wholesale on every
-- sync (see dao.SetPushRegistrations) rather than upserted in place - this
-- data only ever mirrors the device's own already-authoritative state, so
-- a full replace is simpler than reconciling adds/removes and self-heals
-- after any partial/stale write. No unique key on endpoint/token: they can
-- be arbitrarily long (real push-service URLs), and InnoDB's 3072-byte
-- key-size limit makes a long varchar column a poor unique-key candidate -
-- a plain, non-unique `key (domain)` is all lookups here ever need anyway.
create table push_registrations
(
  `domain`            varchar(150) not null,
  `vapid_public_key`  varchar(255) not null default '',
  `vapid_private_key` varchar(255) not null default '',

  primary key (`domain`)
) engine=InnoDB;

create table push_apns_tokens
(
  `domain` varchar(150) not null,
  `token`  varchar(255) not null,

  key (`domain`)
) engine=InnoDB;

create table push_web_subs
(
  `domain`   varchar(150) not null,
  `endpoint` varchar(1000) not null,
  `p256dh`   varchar(255) not null,
  `auth`     varchar(255) not null,

  key (`domain`)
) engine=InnoDB;

-- Failed/suspicious bridge-registration attempts per device (issue #8:
-- "logging issues to see if there is someone trying to hack into the
-- device"). owner_uuid_attempted is whatever the client claimed, which
-- may not match the real owner - that mismatch is exactly what's
-- interesting here.
create table auth_events
(
  `uuid` varchar(64) not null,
  `domain` varchar(150) not null,
  `owner_uuid_attempted` varchar(64) not null,
  `remote_addr` varchar(64) not null,
  `dt` datetime not null,
  `reason` varchar(255) not null,

  unique (`uuid`),
  key (`domain`),
  key (`dt`)
) engine=InnoDB;
