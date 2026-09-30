# Bridge node capacity

What one bridge node can handle, measured on the production hardware
(issue #144). Rerun with `bridge/loadtest` (see "How it was measured") after
any change that could move these numbers, and add a dated section below.

## Summary (2026-10-01)

Per node (OVH Kimsufi KS-5-B: Xeon E5-1650 v4, 6 cores / 12 threads,
128 GB RAM, 2 x 450 GB NVMe in RAID1, 500 Mbit/s public bandwidth):

| What | Capacity per node | Limited by |
|---|---|---|
| Idle connections (devices' pools, open apps, friends) | ~48 KB each: 60,000 held in 2.9 GB; RAM allows over a million, the open-files limit (1,048,576) caps it at ~500,000 paired sessions | memory, file descriptors |
| New connections (TLS handshakes) | ~1,500/s using ~3.5 cores | CPU |
| Small relayed messages (256 B answers) | **~130,000/s**, p50 0.4 ms, p99 < 2 ms at 100 clients | CPU (~10 of 12 cores) |
| Large transfers (1 MB chunks, up or down) | ~60 MB/s each way (~480 Mbit/s) | OVH's 500 Mbit/s public cap |
| MySQL commits (primary) | ~22,500 synced 16 KiB writes/s | NVMe fsync |
| Disk random reads | ~200,000 x 4 KiB IOPS | NVMe |

Two nodes behind DNS round robin double everything except MySQL, which is
one primary for the whole cluster (bridge1) - fine, since the bridge now
writes to it once per device every 10 s, not once per message.

Network between the nodes: 0.35-0.55 ms round trip over WireGuard, and
~495 Mbit/s each way node to node (the same public cap). A client that lands
on the node without its device is forwarded over that link, so a large
transfer that crosses nodes is capped by it twice (once in, once out).

## The fix the test found

Before 2026-10-01 every relayed message ran one `INSERT ... ON DUPLICATE KEY
UPDATE` on `device_metrics` (the same row per device and hour) on the single
MySQL primary:

| 256 B messages | before | after batching |
|---|---|---|
| 100 clients | 8,237/s, p50 8.1 ms, p99 57 ms | 133,130/s, p50 0.42 ms, p99 1.7 ms |
| 500 clients | 9,326/s, p50 33 ms, p99 261 ms | 131,141/s, p50 0.61 ms, p99 205 ms |
| 2,000 clients | 11,272/s, p50 70 ms, p99 976 ms | 124,592/s, p50 0.42 ms, p99 230 ms |
| mysqld CPU | ~550% | 9-30% |
| bridge CPU | 250-350% | 920-1,110% |

`dao.RecordDeviceActivity` now adds up requests and bytes in memory, and
`FlushMetrics` writes one row per device and hour every 10 s (and on
shutdown); a failed write is kept for the next flush. The admin panel's
hourly traffic figures lag by at most 10 s.

## Raw results (2026-10-01)

Load generator on bridge1, target a separate test bridge on bridge2 (its
own `otc_loadtest` database, open registration, port 9443), over the public
network. Each line samples the bridge's and mysqld's CPU every 2 s (`top`).

```
idle connections: 10000   10000 open in 6.8 s (1481/s), all alive after 15 s
                          bridge max CPU 324%, max RSS 531 MB
idle connections: 30000   30000 open in 20.1 s (1495/s), all alive
                          bridge max CPU 347%, max RSS 1536 MB
idle connections: 60000   60000 open in 52.8 s (1137/s), all alive
                          bridge max CPU 356%, max RSS 2867 MB

(metrics written per message)
small, 100 clients    8237 req/s   2.2 MB/s  p50 8.08 ms  p99 56.8 ms   mysqld 517%
small, 500 clients    9326 req/s   2.5 MB/s  p50 32.5 ms  p99 261 ms    bridge 252%, mysqld 546%
small, 2000 clients  11272 req/s   3.0 MB/s  p50 70.1 ms  p99 976 ms    bridge 348%, mysqld 554%
1 MB down, 50 clients   58 req/s  60.5 MB/s  p50 778 ms   p99 2.70 s    bridge 81%,  mysqld 16%
1 MB up, 50 clients     57 req/s  60.2 MB/s  p50 791 ms   p99 2.52 s    bridge 90%,  mysqld 18%

(metrics batched)
small, 100 clients  133130 req/s  36.2 MB/s  p50 0.42 ms  p99 1.69 ms   bridge 955%,  RSS 36 MB,  mysqld 9%
small, 500 clients  131141 req/s  35.5 MB/s  p50 0.61 ms  p99 205 ms    bridge 922%,  RSS 87 MB,  mysqld 21%
small, 2000 clients 124592 req/s  33.7 MB/s  p50 0.42 ms  p99 230 ms    bridge 1110%, RSS 302 MB, mysqld 30%

iperf3 bridge1 <-> bridge2 (public IPs, 4 streams): 495 / 497 Mbit/s
fio: 16 KiB write + fdatasync: 22,630/s (bridge1), 22,444/s (bridge2)
fio: 4 KiB random read, depth 32: 203,342 IOPS (bridge1), 197,539 (bridge2)
```

No errors in any run. The generator shared bridge1 with the production
bridge and the MySQL primary, so these are lower bounds.

## Reading the numbers

- **What a node will run out of first** is CPU for small messages (at
  ~130,000/s), and the 500 Mbit/s link for photos and videos (~60 MB/s).
  For the apps' real mix - mostly small requests, and file transfers in
  4 MB chunks - bandwidth is the practical limit: ~15 x 4 MB chunks/s per
  node.
- **Connections are cheap.** Every device keeps a pool of ~5 connections and
  every open app one more; at ~48 KB each, 10,000 devices with their apps
  open is ~3 GB of the 128 GB.
- **A node restart** hands its connections to the other node: at ~1,500
  handshakes/s, 60,000 connections come back in about 40 s (the listen
  backlog is 65,535, `/etc/sysctl.d/90-otc-bridge.conf`).
- **Disk health.** bridge2's two NVMe drives report 72-74% of their rated
  endurance used (bridge1's: 2%), and the old KS-B running Redis has one
  ~12-year-old SATA SSD at 61% wear. The bridge writes little, but watch
  `sudo smartctl -A /dev/nvme0n1 | grep "Percentage Used"` on bridge2; the
  MySQL primary stays on bridge1.

## Not measured yet

- Relaying across nodes (client on one node, device on the other): same
  code path plus a reverse-proxy hop over WireGuard; expect the CPU cost on
  both nodes and the link cap to bite first for large transfers.
- Many devices reconnecting at once to a restarted node, end to end.
- The Redis node under load (every node refreshes its claims every 20 s: a
  pipeline of one HSET + EXPIRE per device it holds).

## How it was measured

1. On the primary: a schema-only copy of the bridge database,
   `CREATE DATABASE otc_loadtest` + `mysqldump --no-data otc | mysql
   otc_loadtest`, granted to the `otc` user.
2. On bridge2: `/etc/otc_loadtest.ini`, a copy of the bridge's config with
   `port=9080`, `ssl-port=9443`, `db=otc_loadtest`,
   `open-registration=true`, `max-connections-per-device=100000` and no
   `[cluster]`; started beside production with
   `systemd-run --unit otc-loadtest --uid ubuntu --gid ubuntu -p
   LimitNOFILE=1048576 /usr/bin/otc_bridge loadtest`; `ufw allow from
   <generator ip> to any port 9443 proto tcp`.
3. On the generator: `sysctl net.ipv4.ip_local_port_range="1024 65535"
   net.ipv4.tcp_tw_reuse=1`, `ulimit -n 1048576`, then
   `bridge/loadtest` (`GOOS=linux go build ./bridge/loadtest`):

   ```
   loadtest -addr 149.202.83.7:9443 idle -n 60000 -hold 15s
   loadtest -addr 149.202.83.7:9443 relay -devices 20 -clients 100 -size 256 -for 20s
   loadtest -addr 149.202.83.7:9443 relay -devices 25 -clients 50 -size 1048576 -for 20s
   loadtest -addr 149.202.83.7:9443 relay -devices 25 -clients 50 -size 0 -upload 1048576 -for 20s
   ```

   Its fake devices register `load<run>-<n>.off-the.cloud` and answer every
   request with `-size` bytes; its clients send requests back to back.
4. Afterwards: stop `otc-loadtest`, remove the ini and the firewall rule,
   `DROP DATABASE otc_loadtest`, reset the generator's sysctls.
