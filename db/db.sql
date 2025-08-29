CREATE USER 'otc'@'localhost' IDENTIFIED BY 'owivFHIJoNhijc@pe$wo';

drop database otc;
create database otc;

use otc;

create table files
(
  `hash` varchar(32) not null,
  `mime` varchar(150) not null,
  `created` datetime not null,
  `modified` datetime not null,
  `path` text not null,
  `size` int not null,

  key (`hash`),
  fulltext key (`path`),
  INDEX USING BTREE (`created`),
  INDEX USING BTREE (`modified`),
  INDEX USING BTREE (`size`)
) engine=InnoDB;

create table files_tags
(
  `tag` varchar(150) not null,
  `tag_type` enum('featured', 'person'),
  `file_hash` varchar(32) not null,

  primary key (`tag`),
  key (`file_hash`),
  foreign key (`file_hash`) REFERENCES files(`hash`)
);
