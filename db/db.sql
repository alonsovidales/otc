CREATE USER 'otc'@'localhost' IDENTIFIED BY 'owivFHIJoNhijc@pe$wo';

drop database otc;
create database otc;

GRANT ALL PRIVILEGES ON otc.* TO 'otc'@'localhost';

use otc;

-- `path` is a bounded varchar, not text: a bare `unique(path)`/`fulltext
-- key(path)` on a TEXT column needs an explicit prefix length (MySQL
-- rejects indexing a full TEXT/BLOB column without one) and is slower and
-- larger than indexing a properly typed column. 768 chars is the most a
-- unique index can cover in utf8mb4 within InnoDB's 3072-byte key limit
-- (768 * 4 bytes/char), comfortably more than any real file path here. The
-- fulltext index on path was dropped too: nothing in the codebase ever
-- does a MATCH/AGAINST query against it - `LIKE`/`REGEXP`/`=` are all it's
-- ever queried with, and those don't use a fulltext index at all.
create table files
(
  `hash` varchar(64) not null,
  `mime` varchar(150) not null,
  `created` datetime not null,
  `modified` datetime not null,
  `path` varchar(768) not null,
  `size` int not null,

  key (`hash`),
  unique (`path`),
  INDEX USING BTREE (`created`),
  INDEX USING BTREE (`modified`),
  INDEX USING BTREE (`size`)
) engine=InnoDB;

create table file_tags
(
  `hash` varchar(64) not null,
  `tag` varchar(150) not null,
  `score` float not null,

  key (`hash`),
  key (`tag`),
  -- Without this, re-tagging a file (any reprocess) accumulated duplicate
  -- rows per (hash, tag) pair, skewing SearchByTags' score/count. AddTags
  -- upserts against it rather than failing an insert.
  unique key (`hash`, `tag`)

  -- No FK to files(hash) on purpose: files.hash is deliberately non-unique
  -- (dedup means several files rows legitimately share one hash), and
  -- InnoDB's FK enforcement on a non-unique referenced column checks only
  -- "does file_tags still have a row for this value" — not "would this
  -- orphan anything given the other files rows sharing it". That rejected
  -- deleting *any* duplicate copy of a tagged file as long as one still
  -- existed, which is every duplicate but the last, every time — a real
  -- "Cannot delete or update a parent row" failure reachable from the
  -- ordinary "delete a synced photo" flow, not a corruption risk. Keeping
  -- file_tags consistent with files (deleting a hash's tags only once its
  -- last files row is gone) is dao.DelFileByPath's job now, done inside a
  -- transaction with `select ... for update` on the ref-count so
  -- concurrent deletes of the same hash's duplicates can't race each
  -- other into leaving orphaned tag rows.
) engine=InnoDB;

create table social_publications
(
  `uuid` varchar(64) not null,
  `dt` datetime not null,
  `text` text not null,
  `likes` int default 0,
  `friend_domain` varchar(128) not null,
  `own_publication` boolean not null,

  INDEX USING BTREE (`dt`),
  unique(`uuid`),
  key(`uuid`)
) engine=InnoDB;

create table social_publication_likes
(
  `uuid` varchar(64) not null,
  `pub_uuid` varchar(64) not null,
  `dt` datetime not null,
  `friend_domain` varchar(128) not null,

  INDEX USING BTREE (`dt`),
  key (`uuid`),
  unique (`uuid`),
  key (`pub_uuid`),

  foreign key (pub_uuid) references social_publications(uuid)
) engine=InnoDB;

create table social_publications_comments
(
  `uuid` varchar(64) not null,
  `pub_uuid` varchar(64) not null,
  `dt` datetime not null,
  `comment` text not null,
  `publisher_name` varchar(64) not null,
  `likes` int default 0,
  -- Issue #43 follow-up: distinguishes a comment the device owner wrote
  -- from one synced in from a friend, mirroring social_publications'
  -- own_publication - needed to know whether "someone liked a comment" is
  -- a comment worth notifying the owner about.
  `own_comment` tinyint(1) not null default 0,

  INDEX USING BTREE (`dt`),
  unique (`uuid`),
  key (`uuid`),
  key (`pub_uuid`),

  foreign key (pub_uuid) references social_publications(uuid)
) engine=InnoDB;

create table social_publication_comment_likes
(
  `uuid` varchar(64) not null,
  `comment_uuid` varchar(64) not null,
  `dt` datetime not null,
  `friend_domain` varchar(128) not null,

  key (`uuid`),
  unique (`uuid`),
  key (`comment_uuid`),

  foreign key (comment_uuid) references social_publications_comments(uuid)
) engine=InnoDB;

create table social_publications_files
(
  `pos` int not null,
  `hash` varchar(64) not null,
  `uuid` varchar(64) not null,
  `mime` varchar(150) not null,
  `created` datetime not null,
  `modified` datetime not null,
  `size` int not null,

  key (`hash`),
  key (`uuid`),

  foreign key (uuid) references social_publications(uuid)
) engine=InnoDB;

create table social_friendship
(
  `domain` varchar(128) not null,
  `status` enum('pending', 'accepted', 'blocked'),
  `name` varchar(250) default null,
  `image` mediumblob default null,
  `text` text,
  `secret` varchar(128),
  `sent` boolean,
  `latest_sync` datetime null,

  primary key (`domain`),
  key (`domain`)
) engine=InnoDB;

create table settings
(
  `device_uuid` varchar(128) not null,
  `subdomain` varchar(128) not null,
  `bridge_secret` varchar(128) not null,
  -- Issue #43: this device's own VAPID keypair for Web Push, generated once
  -- on first use (see push.Init) - self-hosted, no third-party account
  -- needed, unlike APNs.
  `vapid_public_key` varchar(255) default null,
  `vapid_private_key` varchar(255) default null
) engine=InnoDB;

create table profile
(
  `name` varchar(250) default null,
  `image` mediumblob default null,
  `text` text
) engine=InnoDB;
insert into `profile` values('Your Name', '', 'Description');

create table shared_links
(
  `uuid` varchar(64) not null,
  `size` int not null,
  `created` datetime not null
) engine=InnoDB;

-- `salt` is nullable on purpose: it's the marker between the two key-
-- derivation schemes session.go supports - NULL means this vault predates
-- salted derivation (see session.New's migration path), non-NULL means the
-- password is run through Argon2id with this salt. New installs always
-- get one from the start.
create table vault
(
  `secret` blob not null,
  `salt` varbinary(32)
);

create table events
(
  `uuid` varchar(64) not null,
  `dt` datetime not null,
  `type` varchar(64) not null,
  `content` text,

  key(`uuid`),
  INDEX USING BTREE (`dt`)
) engine=InnoDB;

-- Issue #43: push notifications when a friend posts.
create table web_push_subscriptions
(
  `endpoint` varchar(512) not null,
  `p256dh` varchar(255) not null,
  `auth` varchar(255) not null,
  `created` datetime not null,

  primary key (`endpoint`)
) engine=InnoDB;

create table apns_tokens
(
  `token` varchar(255) not null,
  `created` datetime not null,

  primary key (`token`)
) engine=InnoDB;