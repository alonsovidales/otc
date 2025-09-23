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
