-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- The admin panel's Growth tab. site_visits holds the public site's
-- anonymous visit counts (/privacy, "Visit counts"): per UTC day, page and
-- source, how many times the page was opened - no address, cookie, browser
-- or account. download_counts holds a daily snapshot of the GitHub release
-- files' download totals (the Raspberry Pi image, the desktop apps, each
-- device release's src.tar.gz). Both are pruned after 25 months.
-- Idempotent: run it on the bridge's database once
-- (`sudo mysql otc < 011-growth.sql`), re-running is harmless. Before
-- deploying the bridge that uses it - that bridge keeps the visits it
-- counts in memory, and logs a failed write every 10 s, until the tables
-- exist.

CREATE TABLE IF NOT EXISTS site_visits
(
  `day` date not null,
  -- The page's address with its language ("/", "/es/",
  -- "/private-social-network"), "/account", "/privacy", "/terms".
  `page` varchar(64) not null,
  -- "ref:<name>" for one of our own links (?ref= or ?utm_source=), the
  -- referring website's name (reddit.com), or '' for none.
  `source` varchar(100) not null default '',
  `visits` int unsigned not null,

  primary key (`day`, `page`, `source`)
) engine=InnoDB;

CREATE TABLE IF NOT EXISTS download_counts
(
  `day` date not null,
  -- The file's name in the "image" and "desktop" releases, or
  -- "src.tar.gz@v<N>" for device release N's source archive.
  `asset` varchar(120) not null,
  -- GitHub's total for the file at the day's last poll.
  `downloads` int unsigned not null,

  primary key (`day`, `asset`),
  -- Each file's last snapshot before a day (the Growth tab's "max(day)
  -- ... group by asset"): one index entry per file, not every row since
  -- the first poll.
  key (`asset`, `day`)
) engine=InnoDB;
