# Bridge cluster (issue #144)

Three OVH Kimsufi servers, no private network between them (no vRack on
Kimsufi), so everything internal runs over a WireGuard mesh.

| Host      | Public IP      | Tunnel     | Role                                      |
|-----------|----------------|------------|-------------------------------------------|
| `redis`   | 51.83.103.72   | 10.10.0.1  | Redis (which node holds each device); the old single bridge |
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
