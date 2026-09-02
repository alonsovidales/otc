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
