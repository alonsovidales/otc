# Bridge cluster (issue #144)

Three OVH Kimsufi servers, no private network between them (no vRack on
Kimsufi), so everything internal runs over a WireGuard mesh.

| Host      | Public IP      | Tunnel     | Role                                      |
|-----------|----------------|------------|-------------------------------------------|
| `redis`   | 51.83.103.72   | 10.10.0.1  | Redis (which node holds each device) and certificate renewal; the old single bridge, cleaned up 2026-10-01 (no bridge, no MySQL; a final dump of the old database is kept root-only in `/root/otc-cluster/` until the nodes are backed up) |
| `bridge1` | 37.187.141.41  | 10.10.0.2  | bridge node + MySQL primary (server-id 1)  |
| `bridge2` | 149.202.83.7   | 10.10.0.3  | bridge node + MySQL replica (server-id 2, read-only) |

SSH: `ubuntu@<host>` with the bridge key (`~/.ssh/id_rsa`, the same as
`off-the.cloud`); password and keyboard-interactive login are off.

## Network

- **WireGuard** `wg0`, UDP 51820, `/etc/wireguard/wg0.conf`; the private
  key is generated on each host (`/etc/wireguard/wg0.key`, 0600) and only
  public keys are exchanged. Every peer has `PersistentKeepalive = 25`.
- **ufw** on every host: deny incoming by default; open to the world
  22/tcp, 80/tcp, 443/tcp; 51820/udp only from the other two public IPs;
  everything on `wg0`.

## MySQL (bridge1 primary, bridge2 replica)

- `/etc/mysql/mysql.conf.d/zz-otc-cluster.cnf` - named `zz-` so it is read
  after Ubuntu's `mysqld.cnf`, whose `bind-address = 127.0.0.1` otherwise
  wins: `bind-address = 127.0.0.1,<tunnel ip>`, `server-id`, binary log,
  `gtid_mode = ON`, `enforce_gtid_consistency = ON`, a 16 GB buffer pool.
- bridge2 also has `zz-otc-replica.cnf`: `read_only`, `super_read_only`.
- Replication: `repl@10.10.0.3` (REPLICATION SLAVE), GTID auto-position,
  `GET_SOURCE_PUBLIC_KEY=1` (the tunnel already encrypts). Its password is
  at most 32 characters - MySQL refuses longer ones for replication.
- Application user `otc@localhost` and `otc@10.10.0.%` on database `otc`;
  created on the primary, so the replica has them too.
- Every generated password lives in `/root/otc-cluster/*.pass` (0600) on
  the hosts that need it: `app.pass`, `repl.pass`, `redis.pass`.
- Both bridge nodes write to the primary (`[mysql] host = 10.10.0.2`).
  Failover is manual for now: on bridge2 `STOP REPLICA; RESET REPLICA ALL;
  SET GLOBAL super_read_only = OFF, read_only = OFF;`, remove
  `zz-otc-replica.cnf`, and point both nodes' `[mysql] host` at 10.10.0.3.

Check replication: `sudo mysql -e 'SHOW REPLICA STATUS\G' | grep -E
'Running:|Behind|Error:'` on bridge2.

## Redis (on `redis`)

`/etc/redis/otc-cluster.conf`, included from `redis.conf`: bound to
127.0.0.1 and 10.10.0.1, `requirepass`, no persistence (`save ""`,
`appendonly no`) - it only says which node holds each device's
connections, which the nodes rewrite every 20 s. `redis-tools` is on both
bridge nodes: `REDISCLI_AUTH=$(sudo cat /root/otc-cluster/redis.pass)
redis-cli -h 10.10.0.1 ping`.

## TLS

One certificate everywhere: `*.off-the.cloud` + `off-the.cloud`, renewed on
`redis` by certbot (`authenticator = dns-ovh`, credentials in
`/root/.ovh.ini`). Its deploy hook
(`/etc/letsencrypt/renewal-hooks/deploy/otc-copy.sh`) installs it into
`/etc/ssl/otc/` (`ssl-cert` / `ssl-key` in `[otc-api]`) and pushes it over
the tunnel to both bridge nodes, which reload it without a restart
(`certReloader`).

## The code (`bridge/cluster`, `api/cluster.go`)

Configured by a `[cluster]` section in the bridge's ini; without it the
bridge runs alone, exactly as before.

```ini
[cluster]
node-id = bridge1
internal-addr = 10.10.0.2:8444   ; the listener other nodes forward to
redis-addr = 10.10.0.1:6379
redis-pass = <redis.pass>
token = <at least 32 characters, the same on every node>

[mysql]
host = 10.10.0.2                 ; the primary, from every node
```

- Redis: `otc:nodes` (node -> internal address) and `otc:dev:<domain>`
  (node -> claim expiry). A node claims a device when it gets its first
  connection, refreshes every 20 s (`cluster.Refresh`), and releases it
  when the last one goes; a claim expires 60 s after its last refresh, so
  a node that dies stops being chosen within a minute. One goroutine
  (`Manager.clusterSync`) writes them all, so a claim and its release
  can't be reordered.
- A request for `<device>.off-the.cloud` - websocket, static asset, media -
  landing on a node without a free connection to that device is
  reverse-proxied whole (websocket upgrades included) to a node that has
  one (`clusterRouter`), with the cluster token and the client's address.
  The internal listener refuses anything without the token, marks what it
  accepts as forwarded (never forwarded again), and the public listeners
  strip those headers from outside requests.
- "Online" (admin panel, account page, the setup wizard's check) is any
  node holding the device; the offline alert is skipped while another node
  holds it, and only one node sends it per outage (`otc:alert:<domain>`,
  cleared when a node holds the device again).
- A domain released, deleted with its account or given a new identity
  stops relaying on every node: the node that handled it publishes the
  domain on the `otc:drop` channel and each node closes its connections,
  paired ones included. For a new identity the message is
  `domain<TAB>owner uuid`, and the new owner's connections stay: its device
  registers as soon as it is told, maybe before a node gets the message.
  Every 20 s each node also checks the devices it holds against MySQL and
  closes those no longer registered, or registered by another owner uuid -
  what a message missed while Redis was away, or the admin panel's delete
  (which publishes nothing), leaves behind.

## Log retention (issue #176)

The bridge logs to journald (IP addresses included), and the privacy notice promises 30 days.
Every node (bridge1, bridge2, redis) has `journald-otc.conf` installed as
`/etc/systemd/journald.conf.d/otc.conf`, followed by `systemctl restart systemd-journald`:

```
for h in bridge1 bridge2 redis; do scp bridge/cluster/journald-otc.conf $h:/tmp/otc.conf && ssh $h 'sudo install -D -m 0644 /tmp/otc.conf /etc/systemd/journald.conf.d/otc.conf && sudo systemctl restart systemd-journald'; done
```

## Capacity and tuning

Measured capacity per node, and how to rerun the load test: `docs/bridge-capacity.md`.
Both nodes have `/etc/systemd/system/otc_bridge.service.d/10-limits.conf`
(`LimitNOFILE=1048576`) and `/etc/sysctl.d/90-otc-bridge.conf`
(`net.core.somaxconn` and `net.ipv4.tcp_max_syn_backlog` at 65535).

## Health and intrusion check (`servercheck.sh`)

Runs on the owner's Mac every 6 hours (launchd job
`~/Library/LaunchAgents/cloud.offthe.servercheck.plist`), not on the
servers: a compromised server could tamper with a check that runs on it.
Read-only over SSH:

- health: services, disk, memory, RAID, SMART health and NVMe wear,
  WireGuard handshakes, MySQL replication, Redis, certificate days left
  (redis's certbot copy, and the one each node actually serves);
- intrusion signs: system files that differ from their packages
  (`dpkg -V`), processes running from executables that no longer exist,
  SSH logins (and from where) and failed attempts, and a security
  fingerprint compared with a baseline kept on the Mac - listening ports,
  uid 0 and login users, every `authorized_keys`/`authorized_keys2` of any
  home, sudoers, sshd config, crontabs, systemd unit files, drop-ins and
  enable links (content, not just names), ufw rules, our binaries; and
  kernel modules that taint the kernel while unsigned or from no package
  (a rootkit - not ZFS, which Ubuntu ships signed);
- from outside: the sites and both nodes answer, DNS, and MySQL, Redis and
  the internal port are closed to the internet.

Each host's check is limited to 240 s on the server (smartctl, mysql and
redis-cli have shorter limits of their own, reported when they run out),
with a 300 s watchdog on the Mac; a check that runs out is reported as a
problem, and its security fingerprint is not compared that time.

Reports: `~/Library/Logs/otc-servercheck/` (`latest.txt`, a month kept); a
macOS notification on any problem; each report emailed through Gmail when
a Gmail app password is in the Keychain (its spaces don't matter):
`security add-generic-password -s otc-servercheck-smtp -a vidales.miguelez@gmail.com -w`.
After a change made on purpose (a new port, a key, a deployed binary):
`bash bridge/cluster/servercheck.sh --accept`. The launchd job runs the
script straight from this checkout, so an update that widens the
fingerprint reaches the next scheduled run: lines of a kind the baseline
has none of yet (the `unit:`/`unit_link:` lines, the first time) are taken
into it and noted in the report, not raised as a problem; new lines of a
kind it already has (an `authorized_keys2`, or a key file in a home the
old version did not look at) are reported - check them and `--accept`.
To see that first comparison before a scheduled run mails it, run the
script by hand after updating. Run now:
`launchctl kickstart gui/$(id -u)/cloud.offthe.servercheck`. Only the scheduled run emails
(the launchd job passes `--mail`); running the script by hand just writes the report and
notifies, so a deploy and its `--accept` don't send a burst of mails. Unsigned kernel modules are
accepted only when they are byte for byte the DKMS build of a packaged source (the Veeam agent's
`bdevfilter`/`veeamblksnap`), and loopback listeners are not part of the fingerprint.

## Rule: no request-to-request state in a node's memory

Round-robin DNS sends consecutive requests of one flow to either node, so
anything one request leaves for a later one lives in MySQL (or Redis):
OAuth states (`oauth_states`), app sign-in codes (`app_signin_codes`, was
in memory until 2026-10-01 - "that sign-in has expired" on Android),
setup tokens, setup beacons. Sessions are signed cookies, checked against
the database's epoch. Per-node memory is fine only for limits that may
count per node (login throttling).
