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
  -- Issue #92: false until this friend's entire pre-existing backlog has
  -- been replayed once (see social.go's updateFriendEvents) - otherwise
  -- accepting a long-time-active friend floods the owner with a
  -- notification for every historical like/comment/post at once.
  `notifications_started` tinyint(1) not null default 0,

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
  `vapid_private_key` varchar(255) default null,
  -- Issue #52: off by default, opt-in - gates whether a NEWLY uploaded
  -- photo gets run through face detection at all. Read once, at upload
  -- time, by files_manager's background processing goroutine - a photo
  -- uploaded while this was off is never revisited later just because it
  -- gets turned on afterward (see people/faces' own doc comment below).
  `face_recognition_enabled` tinyint(1) not null default 0
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

-- Issue #78: the owner-facing notification timeline (bell icon) - distinct
-- from `events` above, which is an outbound write-log friends pull from to
-- sync their cached copy of this device's activity, not something the
-- owner ever reads directly. This device has exactly one owner (see
-- `profile`), so there's no recipient/user column to filter by - every row
-- here is already "for" the one person who'll ever see it.
create table notifications
(
  `uuid` varchar(64) not null,
  `dt` datetime not null,
  -- LikePublication|LikeComment|NewComment|FriendRequest|FriendAccepted
  `type` varchar(32) not null,
  `actor_name` varchar(255) not null,
  `actor_domain` varchar(128) not null,
  -- set for LikePublication/NewComment/LikeComment (LikeComment's own
  -- comment_uuid resolves to a pub_uuid too, via dao.GetCommentPubUuid, so
  -- a click can always land on "the post" regardless of which of these
  -- three it is); null for FriendRequest/FriendAccepted.
  `pub_uuid` varchar(64) default null,
  -- set only for LikeComment/NewComment, to additionally scroll to/
  -- highlight the specific comment once the post is open.
  `comment_uuid` varchar(64) default null,
  `acknowledged` tinyint(1) not null default 0,

  unique(`uuid`),
  INDEX USING BTREE (`acknowledged`),
  INDEX USING BTREE (`dt`)
) engine=InnoDB;

-- Issue #82: multiple OTC "users" on one device - each one a fully
-- separate `otc` process (own port, own `otc_<uuid>` database, own
-- storage directory), spawned/monitored by the primary process's
-- supervisor (see supervisor/supervisor.go). This table only ever holds
-- real rows in the PRIMARY database - a per-user database gets this same
-- schema loaded too (see db/schema.go's embedded copy, used to provision
-- a new user's database), but its own copy just sits empty/unused, since
-- only the primary instance's Settings screen ever manages users (see
-- ReqGetInstanceRole).
create table users
(
  `uuid` varchar(64) not null,
  `username` varchar(64) not null,
  `port` int not null,
  `db_name` varchar(64) not null,
  -- Password for the dedicated MySQL user provisioned alongside db_name
  -- (see dao.ProvisionUserDatabase) - re-read on every respawn to
  -- re-render that user's own ini identically (supervisor.renderUserConfig),
  -- since the MySQL user's actual password never changes after creation.
  -- Plaintext, same convention this codebase already uses for
  -- bridge_secret/social_friendship.secret below - protected by DB access
  -- control, not app-level encryption.
  `db_pass` varchar(128) not null,
  `storage_path` varchar(255) not null,
  `subdomain` varchar(128) not null,
  `bridge_secret` varchar(128) not null,
  -- Shared local secret for this user's own /internal/metrics endpoint -
  -- only the supervisor ever sends it (see api's metrics handler), never
  -- exposed to any client.
  `supervisor_token` varchar(128) not null,
  -- Issue #89: deliberately no is_admin/promote column here - the
  -- original device owner (whoever is logged into the primary instance)
  -- is the only admin there will ever be; every other user is just a user.
  -- The supervisor re-checks this before every respawn - deactivating a
  -- user stops its crash-loop-retry cleanly without deleting its data,
  -- unlike a full delete (which also drops the database and storage).
  `active` tinyint(1) not null default 1,
  -- Issue #103: whether this user asked for bridge access at creation.
  -- 0 means local-only - nothing registered on the bridge, and no
  -- bridge-addr rendered into its own config, so its instance never dials
  -- out. Defaults to 1 because every user created before this existed did
  -- get a bridge registration.
  `bridge_access` tinyint(1) not null default 1,
  `created` datetime not null,

  unique (`uuid`),
  unique (`username`),
  unique (`port`)
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

-- Issue #52: face recognition ("People" search), humans only. A person is
-- just a name attached to a set of face detections - never the raw
-- embeddings themselves, deliberately: if the detection/recognition models
-- are ever swapped for better ones, every face gets re-embedded from its
-- original photo (always kept, at files.hash) and re-clustered, but the
-- *names* a person already gave their people survive that untouched, since
-- naming a person and clustering their faces are two independent concerns
-- here. `name` empty means "detected, not yet named" - still shown in the
-- People list so a face can be named/deleted, just without a label yet.
create table people
(
  `id` varchar(36) not null,
  `name` varchar(150) not null default '',
  `created` datetime not null,
  -- This person's medoid face (see face_recognition.MedoidAndCohesion) -
  -- the most representative face already on file, used as ListPeople's
  -- cover thumbnail. Recomputed by files_manager.processFaces every time
  -- a new face is added to this person. NULL until their first face (or
  -- for a person that predates this column) - ListPeople falls back to
  -- their oldest face in that case, same as the original behavior.
  `cover_face_id` varchar(36) default null,
  -- The medoid's own cohesion score (that face's average cosine
  -- similarity to every other face this person has) - see
  -- face_recognition.MedoidAndCohesion's doc comment for why this exists:
  -- matchOrNewPerson clusters by nearest-neighbor, so a "person" can
  -- accumulate a high face count purely by chaining through marginal
  -- matches without any of them actually being mutually alike. NULL
  -- alongside cover_face_id until this person's first face, or for one
  -- that predates this column - ListPeople treats NULL as "not
  -- disqualified" rather than penalizing it.
  `cohesion` float default null,

  primary key (`id`)
) engine=InnoDB;

-- One row per detected face (not per photo - a group photo has one row per
-- person in it). `person_id` is set the moment a face is detected (see
-- files_manager.processFaces): matched to an existing person via
-- face_recognition.IsSamePerson against every other face already on
-- record, or, when nothing matches closely enough, a freshly created
-- (unnamed) person. `thumbnail` is the small aligned face crop the
-- recognizer itself already produces, stored once here so the People list/
-- a person's detail view never needs to re-fetch and re-crop the original
-- photo just to show a face. Deleting a person (issue #52: "just click on
-- delete the individual") deletes every face row here that pointed to
-- them, not just the `people` row - see dao.DeletePerson.
create table faces
(
  `id` varchar(36) not null,
  `hash` varchar(64) not null,
  `person_id` varchar(36) not null,
  `bbox_x` int not null,
  `bbox_y` int not null,
  `bbox_w` int not null,
  `bbox_h` int not null,
  `embedding` blob not null,
  `thumbnail` mediumblob not null,
  `created` datetime not null,

  primary key (`id`),
  key (`hash`),
  key (`person_id`)
) engine=InnoDB;

-- Issue #73: tracks a full-library reprocess run (re-running tagging/face
-- detection against every already-uploaded photo/video, e.g. after a model
-- change like the detection fixes that motivated this) - a singleton row
-- (`id` is always 1, enforced by the primary key rather than a second
-- table), since this device has exactly one owner/library, never several
-- concurrent runs.
--
-- `status` is one of 'idle' (never run, or the UI's default before the
-- first read), 'running', 'completed', 'failed'. `last_hash` is the resume
-- checkpoint - files are walked in a stable order (`hash` ascending, see
-- dao.ListMediaForReprocess), so "continue where it was left" is just
-- "hash > last_hash". This is what makes resuming after an interruption
-- possible at all: the owner's encryption key only ever lives in the
-- memory of the session that started/resumed the run (see
-- files_manager.Reprocess's own doc comment) - if the server restarts
-- mid-run, the goroutine and its key are both gone, but this row survives
-- and tells the next run exactly where to pick back up, without redoing
-- (or re-wiping) work already done in an earlier segment of the same run.
-- A *fresh* run (started from 'idle'/'completed'/'failed') is the only
-- time file_tags/faces/people get wiped first - see the issue's own
-- "strip pre-existing data so it can be recalculated" requirement.
create table reprocess_state
(
  `id` tinyint not null default 1,
  `status` varchar(20) not null default 'idle',
  `total` int not null default 0,
  `processed` int not null default 0,
  `last_hash` varchar(64) not null default '',
  `started` datetime default null,
  `updated` datetime default null,

  primary key (`id`)
) engine=InnoDB;
insert into `reprocess_state` (`id`) values (1);
-- Issue #115: image groups (albums). Files are members by `hash`, the same
-- way file_tags and faces reference them - dedup means several files rows
-- can share one hash, and a group holding the picture should hold it once
-- however many copies exist on disk. No FK to files(hash) for the same
-- reason file_tags has none (see its comment): a hash whose last copy is
-- deleted just stops matching the join in dao.searchMediaClauses, and
-- ListImageGroups only ever counts/covers members that still exist.
create table image_groups
(
  `id` varchar(36) not null,
  `name` varchar(150) not null,
  `created` datetime not null,

  primary key (`id`)
) engine=InnoDB;
create table image_group_files
(
  `group_id` varchar(36) not null,
  `hash` varchar(64) not null,
  `added` datetime not null,

  primary key (`group_id`, `hash`),
  key (`hash`)
) engine=InnoDB;
