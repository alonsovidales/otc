# Record of processing activities (GDPR Art. 30)

Off The Cloud, as controller, for the bridge service at off-the.cloud. Internal record, kept up to
date with the privacy notice (`bridge/static/privacy.html`) and the code. Last reviewed:
8 October 2026.

**Controller:** Off The Cloud (run by a private developer, not yet a registered business),
info@off-the.cloud.
**Data protection officer:** none (not required: no large-scale monitoring or special
categories).
**Data subjects:** bridge account holders; people who use the contact form or write to
info@off-the.cloud; the owners of devices that connect to the bridge.

Not covered here: what owners store on their own devices (photos, files, faces, posts). That data
lives on hardware the owner runs, Off The Cloud has no access to it, and the owner is responsible
for it. The bridge relays connections to devices in memory and stores none of their content.

## Processing activities

| # | Activity | Personal data | Purpose | Legal basis | Retention | Where (code) |
|---|---|---|---|---|---|---|
| 1 | Accounts | email, name, surname, country, bcrypt password hash, linked Google/Apple identity, created and last-seen dates, terms version and acceptance date | Giving owners device names and letting them manage them | Contract, Art. 6(1)(b) | Until the account is deleted, or 6 months unused (no sign-in, no client reaching its devices; warned by email a month before, `accounts/inactivity.go`) | `accounts`, `account_logins` tables; `bridge/accounts` |
| 2 | Device names | domain, device identity and secret, account, switched-off state, last time a client reached it | Routing connections to the right device, protecting the name | Contract | Until released or the account is deleted; released names reserved 30 days | `devices`, `released_domains` |
| 3 | Traffic metrics | per device and hour: request and byte counts (no content) | Running the service, finding abuse | Legitimate interest, Art. 6(1)(f) | 90 days | `device_metrics` |
| 4 | Security log | rejected device connections with IP address | Detecting attempts to take over a device name | Legitimate interest | 90 days | `auth_events` |
| 5 | Push delivery | APNs/FCM tokens, Web Push subscriptions; notification text in transit only | Passing the device's notifications on to the owner's phones; the device-offline alert to phones and browsers | Contract | Until the device removes them or the name is released; text not stored | `push_registrations`, `push_apns_tokens`, `push_fcm_tokens`, `push_web_subs` |
| 6 | Setup and sign-in codes | short codes linking a device or app to an account | Device setup | Contract | 15 minutes, or until used | `account_tokens`, `app_signin_codes` |
| 7 | Email links | SHA-256 of verification and reset links | Proving the email; account recovery | Contract | 48 h (verification), 1 h (reset), or until used | `account_email_tokens`; `bridge/mailer` |
| 8 | Setup beacon | a device's LAN address during setup, under a random setup token; sent by every device set up from the image, with or without an account | Letting the setup page find the device again | Contract; legitimate interest for a setup without an account | 10 minutes | `setup_beacons` |
| 9 | Logs sent by owners | last part of the device's logs (may include file names, search words, Wi-Fi names, addresses), note, account email | Support the owner asked for | Consent, Art. 6(1)(a), given per send | Until the problem is solved, at most 12 months | emailed to info@off-the.cloud (Proton) |
| 10 | Contact messages | name, email, message | Answering | Legitimate interest | Until closed, at most 12 months (pruned automatically) | `contact_requests` |
| 11 | Server logs | technical errors with IP addresses | Keeping the service working and secure | Legitimate interest | 30 days (journald `MaxRetentionSec`, `bridge/cluster/journald-otc.conf`) | journald on the bridge nodes |
| 12 | Backups | copies of the database (all of 1-10) | Recovering from failure | Legitimate interest | OVH backup retention | OVH Veeam backups of the nodes |

## Recipients and processors

| Who | Role | Data | Where | Transfer basis |
|---|---|---|---|---|
| OVH | Processor: hosting (bridge1, bridge2, redis), DNS, backups | everything above | EU (France) | n/a. **Art. 28 DPA: OVH's contract terms (check it's accepted in the control panel)** |
| Proton | Processor: sending email, the info@ mailbox | emails, names, links, sent logs, messages | Switzerland | EU adequacy decision |
| Apple | Sign in with Apple; APNs push delivery | identity, email, name; push tokens and notification text | US | EU-US Data Privacy Framework |
| Google | Google sign-in; Firebase Cloud Messaging | identity, email, name; push tokens and notification text | US | EU-US Data Privacy Framework |
| Browser push services (Google, Mozilla, Apple, Microsoft) | Deliver the device-offline Web Push to browsers | push subscription endpoint; notification text, encrypted end to end to the browser | US | EU-US Data Privacy Framework (check each certification) |
| GitHub | Hosts software and update downloads (devices fetch directly) | the device's IP address | US | EU-US Data Privacy Framework |

No other recipients, unless the law requires it.

## Security measures (Art. 32)

- TLS on every connection to the bridge, and between the nodes over WireGuard. MySQL and Redis
  listen only on the WireGuard network.
- Account passwords: bcrypt (cost 12). Email and setup tokens are stored as hashes. Device
  passwords never reach the bridge in readable form (RSA-OAEP to the device's key).
- SSH keys only, firewall (ufw) on every node, a security fingerprint of each node checked from
  outside every 6 hours (`bridge/cluster/servercheck.sh`), and admin access behind its own login.
- Rate limits on sign-in, sign-up, request sizes and per-device traffic (`bridge/limits`).
- Owners can export and delete their account themselves (account page).
- Retention enforced in code: the pruner in `bridge/dao` (metrics, security log, contact
  messages) and the token expiries.

## Reviews

Review this record whenever the bridge starts storing something new, a processor changes, or
the privacy notice changes, and at least once a year.
