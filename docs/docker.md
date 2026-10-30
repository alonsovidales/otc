# Off The Cloud on a home server (Docker, beta)

On a Raspberry Pi, use the Off The Cloud image instead: it builds and watches the disk mirror for
you. The Raspberry Pi is the main way to run Off The Cloud, and the
[main README](../README.md) explains how to set one up, under "Installation of the device".

Off The Cloud gives your family its own private social network and a home NAS for your photos and
files. You share photos and videos with the friends you accept, from device to device, with no
ads, no algorithm and no strangers. The phone apps back up your photos and videos in full
resolution, the computer apps keep folders in sync, and Files works like a drive in the apps and in
any browser. Everything you store is encrypted at rest under a key only your password unlocks.

This guide is for people who already run a Linux home server, a NAS or a Docker host and want
Off The Cloud on it, as a container. The container image is a **beta**.

## What the server looks after

The container runs the same device software as a Raspberry Pi. A few things are the server's job
instead:

- **Your files need redundant storage: the server's, or two disks the container mirrors.** On a Pi,
  Off The Cloud mirrors two disks and tells you when one fails. In a container it keeps everything in one
  folder on the server, so keep that folder on redundant storage: your NAS's RAID, a ZFS or Btrfs
  mirror, or an Unraid pool. Storage health in the apps then shows no mirror, and your server's own
  alerts tell you about a failing disk. Or give the container two disks of their own: it mirrors and
  watches them as a Pi does (see [Two disks, mirrored](#two-disks-mirrored-optional)). Keep a backup
  too (see [Backup and restore](#backup-and-restore)).
- **Wi-Fi and disks belong to the server.** The first run skips the storage step (the mirror's
  disks, if you want one, are named in `.env`), and Settings has no Wi-Fi section.
- **Updates come by pulling the image.** The device tells you when a new release is out, and you
  pull it on the server (see [Updating](#updating)).
- **It needs Linux and host networking.** The container shares the server's network, so the apps
  can find it at home. Docker Desktop is not supported.
- **It starts as a new device.** Moving a Raspberry Pi's data into the container is not supported in
  the beta.

## What you need

- A 64-bit Linux server with Docker Engine and Docker Compose (the `docker compose` command). The
  image is built for `linux/amd64` and `linux/arm64`. On amd64 the processor needs SSE4.1; the
  container checks this when it starts.
- At least 4 GB of memory and 2 CPU cores for the container, and 8 GB to run everything at once. The
  compose file gives it 8 GB (see [Memory](#memory)).
- Room for your photos and files on redundant, **local** storage. The database lives in the same
  folder, and it can't run on a network or FUSE share (NFS, SMB, Unraid's `/mnt/user`). Or two empty
  disks for the container to mirror: the mirror holds as much as the smaller one.
- About 1.4 GB more for the models: the photo-tagging and face models, which the first start
  downloads from huggingface.co and github.com, and Smart search's (about 400 MB), which the device
  downloads from github.com in the background once it runs (Smart search is on unless you turn it
  off in Settings).
- Free ports on the server: 8080 and 8443 for the web app and the apps, and 3316 on 127.0.0.1 for
  the built-in database. You can move any of them.

## Install

1. Make a folder for the compose file, then download it and the settings example into it:

   ```
   mkdir -p ~/otc && cd ~/otc
   curl -fsSLO https://raw.githubusercontent.com/alonsovidales/otc/main/docker/compose.yaml
   curl -fsSL -o .env https://raw.githubusercontent.com/alonsovidales/otc/main/docker/env.example
   ```

2. Open `.env` and set `OTC_DATA` to the folder that will hold everything: photos, files and the
   database. Pick an empty folder, or one that doesn't exist yet, on your redundant storage:

   ```
   OTC_DATA=/tank/otc
   ```

   If another program already uses one of the ports, change it here too. To have the container
   mirror two disks instead, set them up now, before the first start (see
   [Two disks, mirrored](#two-disks-mirrored-optional)).

3. Start the device and follow its log:

   ```
   docker compose up -d
   docker logs -f otc
   ```

   The first start downloads about 900 MB of models, with its progress in the log, and creates the
   database. Smart search's model (about 400 MB) follows in the background once the device runs. Then the log says until when you can choose the owner password:

   ```
   [otc] open http://<this server>:8080 from your home network before 14:05 UTC to choose the owner password
   ```

   `docker ps` shows the container as `healthy` once the device answers.

## Choose the owner password

Before the time in the log (30 minutes after the device first starts), open
`http://<server-ip>:8080` in a browser on a computer or phone on your home network. Use the
server's own address, not a reverse proxy. The web app asks for your name and the owner password
(8 characters or more), then signs you in.

The password encrypts your files and can't be recovered. Keep it somewhere safe.

The device takes its first password only this way: from your home network, straight to its own
port, within those 30 minutes. Restarting the container doesn't give you more time.

**Missed the 30 minutes?** Set the password on the server instead. Put it on one line of a file
that only you can read, then run:

```
docker exec -i otc otc-entrypoint set-password < password.txt
rm password.txt
```

Then sign in at `http://<server-ip>:8080` and add your name in Settings › Profile.

**Setting up without a browser?** The container can set the password as it starts, from a Docker
secret. Uncomment the `OTC_OWNER_PASSWORD_FILE` line in `compose.yaml`, and add the secret to the
service and at the end of the file:

```yaml
services:
  otc:
    # ... the rest of the service as it is ...
    environment:
      OTC_OWNER_PASSWORD_FILE: /run/secrets/owner_password
    secrets: [owner_password]

secrets:
  owner_password:
    file: ./owner_password.txt
```

Once the device has its password, remove those lines and the file, and run `docker compose up -d`.
Until you do, the log warns about it at every start.

## Connect

### At home

Right after setup the device works on your home network only, like a Raspberry Pi set up with
"Continue without an account".

- **Browser:** `http://<server-ip>:8080`.
- **Phone and Mac apps:** turn on **Custom address** where you enter the device's name, and type
  `ws://<server-ip>:8080/ws`.
- **otc-sync:** `otc-sync settings --address ws://<server-ip>:8080/ws --password-prompt`.

Give the server a fixed address in your router, so that address keeps working.

### From anywhere, with friends: the bridge

In the web app, open Settings › Bridge access. Sign in with your Off The Cloud account, or paste a
setup code from [off-the.cloud/account](https://off-the.cloud/account), choose a name and press
**Join the bridge**. The device restarts in a few seconds. It is then at
`https://<name>.off-the.cloud` from anywhere, and in the apps you type just the name.

The bridge brings the social side: friends, posts, comments, share links that work from anywhere,
and push notifications. [Bridge accounts](../README.md#bridge-accounts) explains the accounts. You
don't open any port on your router: the device connects out to the bridge. At home, the apps then
talk to the server directly over its encrypted port (8443), on their own.

### More users

Each user you add in Settings › Users gets ports of their own on the server: the next free one
after 8080 (8081, 8082 and so on) for the web app and the apps, and that port plus 363 (8444, 8445
and so on) for the apps at home. Ports that are already in use are skipped.

### Firewall

If the server has a firewall, let your home network reach 8080 and 8443, and the users' ports. Don't
forward these ports on your router: the device's own port is plain HTTP. For an address of your own
on the internet, put a reverse proxy in front (see [Your own domain](#your-own-domain)).

## Your own domain

You can also reach the device at a domain of your own, through a reverse proxy such as Caddy,
Traefik or Nginx Proxy Manager. The bridge stays the way your friends reach your posts.

1. Choose the owner password first, straight at `http://<server-ip>:8080`. The device refuses a
   first password that comes through a proxy.
2. Point the proxy at `http://<server-ip>:8080`, with:
   - WebSocket upgrades passed through;
   - no read timeout on `/ws` (the apps keep that connection open);
   - no response buffering on `/media/` (videos stream from there).

   With Caddy on the server itself, for example:

   ```
   photos.example.com {
       reverse_proxy 127.0.0.1:8080 {
           flush_interval -1
       }
   }
   ```

3. Tell the device where the proxy connects from, so that it limits password attempts for each
   visitor rather than for everyone behind the proxy at once. In `compose.yaml`, uncomment
   `OTC_TRUSTED_PROXIES` and set it to the proxy's address or range: `127.0.0.1` for a proxy on the
   server itself, or its Docker network's range, such as `172.18.0.0/16`, for a proxy in a
   container. Then run `docker compose up -d`.
4. Open `https://photos.example.com` in a browser. In the apps, turn on **Custom address** and type
   `wss://photos.example.com/ws`.

## Everyday commands

Run these in the folder with `compose.yaml`:

```
docker compose stop       # stop the device cleanly (it can take up to 2 minutes)
docker compose start      # start it again
docker compose restart    # restart it
docker logs -f otc        # the container's log
```

Settings › Restart Device restarts the device's software; the container and the database keep
running. The device's own log is in Settings › Logs, and on the server in
`<your folder>/state/log/otc.log` (only root can read it). To copy it into `docker logs` as well,
add `OTC_LOG_STDOUT: "1"` under `environment:`.

## Updating

A new image is published for every device release, once it passes start-up and sign-in tests. The device tells you about a release only when its image is there: Settings › Device
version shows it, with the commands to run in place of an Update button. For a major or critical
release, Alerts say so too, and a critical one also sends a push notification. To update, run in
the folder with `compose.yaml`:

```
docker compose pull
docker compose up -d
```

The image is `ghcr.io/alonsovidales/otc`, with these tags:

| Tag | What you get |
|---|---|
| `beta` | The newest release. `compose.yaml` uses it. |
| `latest` | The same image as `beta`. |
| A build number, such as `152` | That release only, so you choose when to update. Settings › Device version shows each release's build number. |

To pin a release, change the `image:` line, for example to `ghcr.io/alonsovidales/otc:152`. To
update a pinned device, change the number, then pull and start it as above. A tool that watches
images, such as Diun or What's Up Docker, can tell you when `beta` changes.

On its first start, a new image brings the database up to date. Settings › Logs › Updates shows how
that went. If it fails, the container stops and says why.

An older image won't start on a folder that a newer release has used, because the database may not
work with older code. The log names the release that last used the folder: use that image or a newer
one. (`OTC_ALLOW_DOWNGRADE: "1"` under `environment:` starts the older one anyway, at the database's
risk.)

To keep a copy of the database from just before an update, run
`docker exec otc otc-entrypoint dump` first.

## Backup and restore

### What to back up

Back up the whole folder (`OTC_DATA`) except `storage/mysql/`. That is where the database's live
files are, and a copy taken while the database runs can't be trusted. Instead, every night at 03:00
the device writes a consistent copy of every database into `storage/db-dumps/<date>/`, and a restore
loads that. It keeps the last 7 days' copies, and always the newest complete one. You can leave out
`state/models/` too: it is downloaded again.

The nightly copy runs at 03:00 in the container's time zone: UTC, unless you uncomment `TZ` in
`compose.yaml`. To make a copy now:

```
docker exec otc otc-entrypoint dump
```

Use your usual backup tool, such as restic, borg or your NAS's own backup app. It has to run as
root, because the files belong to the container's own users (see
[Folders and file owners](#folders-and-file-owners)). A ZFS or Btrfs snapshot of the folder makes a
good source for the backup: the nightly copies in it are consistent.

You can also stop the device, copy the whole folder, database included, and start it again:

```
docker compose stop
sudo cp -a /tank/otc /backup/
docker compose start
```

To restore a copy made this way, put the folder back and run `docker compose up -d`.

**With two disks mirrored**, your files and the database are on the mirror, which only the container
sees: `storage/` in your folder stays empty. A mirror is not a backup (a file you delete is gone
from both disks), so back it up through the container, for example onto another disk:

```
docker exec otc tar -C /mnt/storage --exclude=./mysql -cf - . > /backup/otc-storage.tar
```

Back up `state/` and `config/` from your folder as above. Putting such a backup back onto a mirror,
or loading a night's copy into one, is not supported in the beta: restore the backup into `storage/`
of a folder without the mirror, as below - after removing the mirror lines from `.env` and deleting
`config/storage-mode` (with `sudo`), which otherwise keeps the device on the mirror.

Keep two things in mind:

- **The password.** Your files are encrypted under a key only your password unlocks, so a backup
  of them is no use without it. Keep the password somewhere safe.
  ([What is encrypted on the device](../README.md#what-is-encrypted-on-the-device) has the details.)
- **`config/` holds secrets.** `config/otc-install.env` has the database password and the device's
  bridge secret. Keep your backups private.

### Restore

To load a night's database copy, after you put the folder back from a backup or to go back to an
earlier night:

1. Stop and remove the container. The folder stays as it is.

   ```
   docker compose down
   ```

2. A copy is loaded into a folder that has no database. If `storage/mysql/` is there, move it out
   of the way:

   ```
   sudo mv /tank/otc/storage/mysql /tank/otc/mysql.old
   ```

3. If you are restoring from a backup, put the folder back, as root, so the files keep their owners.
   If your backup tool didn't keep the owners, give the files back to the device:

   ```
   sudo chown -R 7310:7310 /tank/otc/storage /tank/otc/state
   ```

4. Load the newest complete copy, or the copy of a given day:

   ```
   docker compose run --rm otc restore
   docker compose run --rm otc restore 2026-10-20
   ```

5. Start the device:

   ```
   docker compose up -d
   ```

The device comes back with your files, collections, friends and owner password. If `config/` was
lost, it starts on your home network only: join the bridge again in Settings › Bridge access, with
the same account and name.

## Folders and file owners

`OTC_DATA` holds three folders:

| Folder | What it holds |
|---|---|
| `storage/` | Your photos and files (encrypted), the database (`storage/mysql/`), the nightly copies (`storage/db-dumps/`) and the certificate the apps trust at home (`storage/.lan-tls/`). This is the folder that must be on redundant storage. With two disks mirrored it stays empty: the mirror holds all of it. |
| `state/` | The device's working files, its log (`state/log/otc.log`) and the models (`state/models/`). |
| `config/` | The device's settings and identity, and the update log. The container rewrites the settings from `compose.yaml` at every start. |

The files belong to fixed user ids: 7310 for the device and 7311 for the database, with `config/`
owned by root. There is no setting to change them. They stay the same from one image to the next, so
the files always keep their owner. Most files can be read only by their owner, so looking at them
or backing them up needs root on the server.

To keep the database on a faster disk, such as an SSD, uncomment the `/mnt/storage/mysql` line under
`volumes:` and point it at a local folder on that disk. That folder holds the database, so it needs
redundancy too. This doesn't go with two disks mirrored: the database lives on the mirror, and the
container refuses to start with that line.

## Two disks, mirrored (optional)

On a server with two disks to spare, the container can mirror them itself, as a Raspberry Pi does. It
builds a mirror (RAID1) on the two disks and keeps your photos, files and the database on it. It
watches the mirror: when a disk stops working, the files stay safe on the other one, and Alerts in
the apps, with a push notification, name the disk. It adds a replacement and copies everything onto
it, and it grows onto bigger disks.

It is for servers whose disks aren't mirrored already. Synology, TrueNAS and Unraid mirror their own
disks: there, keep the folder on their pool instead.

Set it up before the first start. A device whose files are in the folder refuses to start with the
mirror lines in `.env`, and one whose files are on a mirror refuses to start without them: moving
the files from one to the other isn't supported yet. The device notes where its files are in
`config/storage-mode`. To start over with a new, empty device on the other storage on purpose,
delete that file (with `sudo`); to start one on the mirror, also move the contents of `storage/`
somewhere else first. The files where they were stay as they are.

1. Find the two disks' names. They are under `/dev/disk/by-id/`, where a disk keeps its name
   whatever order the server finds its disks in:

   ```
   ls -l /dev/disk/by-id/
   ```

   Pick the names of whole disks, not the ones ending in `-part1`, `-part2` and so on. `lsblk` shows
   which disk is which (`sda`, `sdb`) and its size. Each disk must be free: not mounted, not used for
   swap, not part of another array.

2. Download the mirror's compose file next to `compose.yaml`:

   ```
   curl -fsSLO https://raw.githubusercontent.com/alonsovidales/otc/main/docker/compose.mirror.yaml
   ```

3. Add these lines to `.env`, with your disks' names (`env.example` has them too):

   ```
   COMPOSE_FILE=compose.yaml:compose.mirror.yaml
   OTC_MIRROR_DISK1=/dev/disk/by-id/ata-WDC_WD40EFRX-68N32N0_WD-WCC7K1111111
   OTC_MIRROR_DISK2=/dev/disk/by-id/ata-WDC_WD40EFRX-68N32N0_WD-WCC7K2222222
   ```

4. Start the device: `docker compose up -d`. The log says what the container did with the disks and
   how the mirror is. A new mirror copies one disk onto the other in the background, which takes
   hours on large disks; the device works meanwhile.

`storage/` in your folder stays empty from then on: the container mounts the mirror over it.
`state/` and `config/` stay in the folder. Without a mounted mirror the container doesn't start, so
the device never writes beside it.

### What is erased

A blank disk (no partitions, no filesystem, nothing at all) is used as it is. A disk that holds
anything is refused: the log says what is on it, and the line that lets the container erase it. Add
that line to `.env` only if nothing on the disk is needed:

```
OTC_MIRROR_ERASE=/dev/disk/by-id/ata-WDC_WD40EFRX-68N32N0_WD-WCC7K2222222
```

Write each name exactly as in `OTC_MIRROR_DISK1` or `OTC_MIRROR_DISK2`, both separated by a space if
both may go, and remove the line once the mirror is built. A disk that is part of another array is
never touched, listed or not, and neither is a mirror another system made (a NAS's, an old server's):
if nothing on it is needed, erase it on the server yourself, with the `wipefs -a` command the log
gives. Disks that already hold this mirror, after the container was made again, are used as they are.
`OTC_MIRROR_ERASE` only takes `/dev/disk/by-id/` names: a name like `/dev/sdc` can belong to another
disk after the server restarts.

### What the mirror's compose file allows

`compose.mirror.yaml` gives the container what building and mounting a mirror needs: the two disks,
the server's md arrays (`device_cgroup_rules`), the `SYS_ADMIN`, `MKNOD` and `SYS_RESOURCE`
capabilities (the last to grow the filesystem while it is mounted), and no AppArmor or SELinux
profile, as both refuse mounting inside a container. With these, root inside the container can manage
disks and arrays, and comes close to root on the server. What runs as root there is the container's
own supervisor (its start-up and the script that watches over the device), the mirror helper, and the
bridge setting's helper when the device asks for it. The device itself, the part the apps and the
bridge talk to, and its database run as unprivileged users with no capabilities, as without the
mirror.

### Replacing a disk

When a disk stops working, the device keeps running on the other one. To replace it:

1. Take out the disk the alert names (the name usually ends in the serial number on the disk's
   label) and put in an empty disk at least as large as the other one. Turn the server off first,
   unless its disks can be swapped while it runs.
2. In `.env`, put the new disk's name in place of the old one, in `OTC_MIRROR_DISK1` or
   `OTC_MIRROR_DISK2`.
3. Run `docker compose up -d`. The container is made again with the new disk, adds it to the mirror
   and copies everything onto it while the device works. Settings › Logs › Device shows the copy's
   progress, and Alerts say when both disks are working again.

While `.env` names a disk the server no longer has, the container doesn't start: Docker refuses it
(`error gathering device information`). Until the new disk is in, write `/dev/null` in its place
(`OTC_MIRROR_DISK2=/dev/null`, say) and run `docker compose up -d`: the device runs on the other
disk alone. A new mirror is never built with a disk set to `/dev/null`.

The container only uses the mirror it started with: if the disks named hold another one, or none,
it doesn't start, so a device never starts over empty on other disks by itself.

### Bigger disks

To make room, replace the disks one at a time with bigger ones, as above, and let the first copy end
before you change the second. Once both are bigger, the device grows the mirror and the filesystem
on it by itself, while it runs. It checks every 10 minutes, never while a disk is being copied.

### If the server has mdadm

A Linux server with mdadm installed may assemble the mirror by itself when it starts. That is fine:
the container takes it over as it is. Never mount the mirror on the server. Two systems writing to
it would damage it, and the container refuses to start while it is mounted anywhere else.

## Settings

Settings go in `.env` (the folder, the ports and the mirror's disks) or under `environment:` in
`compose.yaml`, where the commented lines are ready to use. After a change, run
`docker compose up -d`.

| Setting | Default | What it does |
|---|---|---|
| `OTC_DATA` | `/srv/otc` | The folder that holds everything. Set in `.env`. |
| `OTC_HTTP_PORT` | `8080` | The web app and the apps. |
| `OTC_LAN_TLS_PORT` | `8443` | The apps' encrypted port at home. |
| `OTC_DB_PORT` | `3316` | The built-in database, on 127.0.0.1 only. |
| `TZ` | UTC | The time zone for the nightly database copy at 03:00, such as `Europe/Amsterdam`. |
| `OTC_MEMORY_GB` | none | Sizes the device below `mem_limit`, in GB. |
| `OTC_TRUSTED_PROXIES` | none | Your reverse proxy's addresses or ranges (see [Your own domain](#your-own-domain)). |
| `OTC_OWNER_PASSWORD_FILE` | none | Sets the first owner password from a file as the container starts. |
| `OTC_LOG_STDOUT` | `0` | `1` copies the device's own log into `docker logs`. |
| `OTC_ALLOW_DOWNGRADE` | `0` | `1` starts an older image on a folder a newer release has used. |
| `OTC_MIRROR_DISK1`, `OTC_MIRROR_DISK2` | none | The two disks to mirror, with `compose.mirror.yaml` (see [Two disks, mirrored](#two-disks-mirrored-optional)). Set in `.env`. |
| `OTC_MIRROR_ERASE` | none | Disks the mirror may erase when they hold something (see [What is erased](#what-is-erased)). Set in `.env`. |

### Memory

`mem_limit: 8g` is how much memory the container may use. Raise it to give the device more. Keep
`memswap_limit` the same as `mem_limit`: then the container has no swap, and decrypted data never
reaches a disk. The `/tmp` line keeps the photos and videos being processed in memory; keep its size
(`4g`) at no more than half of `mem_limit`, because what is in `/tmp` counts as the container's
memory.

With less than 6 GB, the device runs in low-memory mode and processes one photo at a time, with
one model loaded at once. With 4 GB, do as on a 4 GB Raspberry Pi: turn **Image Tagging** and
**Face Recognition** off in Settings before you back up your photos, and **Smart search** too if
the device struggles.

## Platforms

Every platform uses the same compose file. It is tested with plain Docker and Compose on Linux. The
notes below say where each platform takes a compose file. They are generic, and have not been tested
on each platform.

On every platform:

- Keep every line of the file. For example, the container refuses to start without the `/tmp` line,
  and it needs `network_mode: host`.
- Where the platform has no place for the `.env` file, write your folder into the three `volumes:`
  lines in place of `${OTC_DATA:-/srv/otc}`, for example `/volume1/otc/storage:/mnt/storage`.
- Use a folder on the platform's redundant storage, never a network share.

| Platform | Where the compose file goes |
|---|---|
| Synology (Container Manager) | Project › Create: choose a folder for the project and paste the file as its `docker-compose.yml`. Keep the data on a volume, such as `/volume1/otc`. |
| TrueNAS (custom apps) | In Apps, install a custom app from YAML and paste the file. Use a dataset on your pool, such as `/mnt/tank/otc`. |
| Unraid (Compose Manager plugin) | In the Docker tab, add a new stack and paste the file as its compose file. Use a pool path, such as `/mnt/cache/otc`, not `/mnt/user/...`: that is a FUSE share, where the database won't run. |
| Portainer (stacks) | Stacks › Add stack: paste the file in the web editor and add `OTC_DATA` under Environment variables. Portainer must manage a Linux Docker host. |
| CasaOS | Add a custom app, choose Import, and paste the file. |

On Synology, TrueNAS and Unraid, leave out `compose.mirror.yaml`: they mirror their own disks.

On other Linux hosts:

- With SELinux (Fedora, RHEL and similar), add `:Z` to the end of the three `volumes:` lines.
- On rootless Docker or Podman, remove the `memlock: -1` line, as its comment says.

## When it won't start

`docker logs otc` says why, and what to change. The messages use the container's own paths:
`/mnt/storage` is your folder's `storage/`, `/var/lib/otc` its `state/` and `/etc/otc` its
`config/`. The most common ones:

| The log says | What to do |
|---|---|
| `port 8080 is already in use on this server: set OTC_HTTP_PORT to a free port` | Another program uses that port (a UniFi controller uses 8080 and 8443, for example). Set the named setting to a free port in `.env`. |
| `/mnt/storage is not a volume` (or `/var/lib/otc`, `/etc/otc`) | A `volumes:` line is missing. Put it back. |
| `/tmp is not a RAM filesystem` | Put the `tmpfs:` lines back. |
| `the database can't live on ..., a ... filesystem (network or FUSE)` | Move the folder to a local disk, or put the database on one with the optional `/mnt/storage/mysql` line. |
| `/mnt/storage holds files of user ...` | The folder holds another install's files. Use an empty folder. |
| `/mnt/storage/mysql holds files of user ...` | The database's files have another owner. If you changed it, the message gives the command that gives it back. |
| `this storage was last used by release ..., newer than this image` | Use that release's image or a newer one (see [Updating](#updating)). |
| `this processor lacks SSE4.1` | The image's photo decoder needs it. Use a server with a newer processor. |
| `could not download ... - trying again in an hour` | The first start couldn't download a model. Check that the server can reach huggingface.co and github.com, then run `docker compose restart`. |
| `MariaDB stopped while starting` | The database's own log is `storage/mysql/mariadb.err`. Two containers can't use the same folder. |
| `the device stopped 5 times in 2 minutes` | The device's log, `state/log/otc.log`, says why. |
| `... is not a disk: check OTC_MIRROR_DISK1 in .env` | The name in `.env` isn't a whole disk. Check it with `ls -l /dev/disk/by-id/` (see [Two disks, mirrored](#two-disks-mirrored-optional)). |
| `... is in use on this server` | The server, or another container, uses that disk: mounted, for swap, or in an array or volume. Free it, or name another disk. |
| `... belongs to ..., another array on this server` | That disk is part of another array, which the container never touches. Do what the message says, or name another disk. |
| `... OTC erases a disk only when .env says so` | The disk isn't blank. The message ends with an `OTC_MIRROR_ERASE` line: add it only if nothing on the disk is needed (see [What is erased](#what-is-erased)). |
| `the mirror is mounted somewhere else on this server` | Unmount it on the server, or stop the other container that uses it. |
| `/mnt/storage/mysql is a volume of its own, which the disk mirror would hide` | Remove the `/mnt/storage/mysql` line from `compose.yaml`: with the mirror, the database lives on it. |

Two warnings don't stop the device, but say that a line of the compose file is missing:
`locked memory is limited` (put back `memlock: -1`) and `this server has swap and the container may
use it` (set `memswap_limit` to the same value as `mem_limit`).

## Feedback

This is a beta. If something doesn't work, or works differently on your platform, open an issue at
[github.com/alonsovidales/otc/issues](https://github.com/alonsovidales/otc/issues) with what
`docker logs otc` says and the platform you use.
