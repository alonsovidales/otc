CREATE USER 'otc'@'localhost' IDENTIFIED BY 'owivFHIJoNhijc@pe$wo';

drop database otc;
create database otc;

GRANT ALL PRIVILEGES ON otc.* TO 'otc'@'localhost';

use otc;

create table files
(
  `hash` varchar(64) not null,
  `mime` varchar(150) not null,
  `created` datetime not null,
  `modified` datetime not null,
  `path` text not null,
  `size` int not null,

  key (`hash`),
  unique (`path`),
  fulltext key (`path`),
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

  foreign key (hash) references files(hash)
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

create table social_publications_comments
(
  `uuid` varchar(64) not null,
  `pub_uuid` varchar(64) not null,
  `dt` datetime not null,
  `comment` text not null,
  `publisher_name` varchar(64) not null,
  `likes` int default 0,

  INDEX USING BTREE (`dt`),
  key (`uuid`),
  key (`pub_uuid`),

  foreign key (pub_uuid) references social_publications(uuid)
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

  primary key (`domain`),
  key (`domain`)
) engine=InnoDB;

create table sent_actions
(
  `uuid` varchar(64) not null,
  `dt` datetime not null,
  `type` varchar(150),
  `target` varchar(150),

  key (`uuid`)
) engine=InnoDB;

create table settings
(
  `device_uuid` varchar(128) not null,
  `subdomain` varchar(128) not null,
  `bridge_secret` varchar(128) not null
) engine=InnoDB;

create table profile
(
  `name` varchar(250) default null,
  `image` mediumblob default null,
  `text` text
) engine=InnoDB;

create table shared_links
(
  `uuid` varchar(64) not null,
  `size` int not null,
  `created` datetime not null
) engine=InnoDB;

create table vault
(
  `secret` blob not null
);
