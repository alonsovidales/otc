#!/usr/bin/env python3
# -*- coding: utf-8 -*-
# SPDX-License-Identifier: AGPL-3.0-or-later
"""
First-boot setup wizard for the OTC image (issue #38).

The flashable image is nothing but stock Raspberry Pi OS plus this service,
the hotspot script (network_setup.py) and their units: everything else a
device needs is installed by scripts/install.sh, fetched fresh from GitHub
at setup time - so there is nothing to keep up to date in the image itself.

This serves a small web page on port 80 (reached over the "Off The Cloud"
hotspot, where captive-portal detection opens it automatically, or at
http://otc.local/ on a wired network) that walks through:

  1. WiFi     - pick a network; the Pi joins it while keeping the hotspot
                up (network_setup.py does the joining), and waits until
                the bridge is reachable.
  2. Storage  - the USB disks found, with sizes: two make a RAID1 mirror,
                none keeps everything on the SD card. If the disks already
                hold an Off The Cloud array with a database on it (a dead
                Pi whose card was re-flashed - the disks survived), the
                person is offered to recover it instead: install.sh
                reassembles the array and the identity in that database
                (device UUID, name, bridge secret) is the one the device
                then presents to the bridge, so no new name is needed.
  3. Name     - fresh devices only: the <name>.off-the.cloud address,
                checked live against the bridge and reserved there, with a
                freshly generated identity, the moment it is confirmed.
  4. Install  - runs install.sh with all of the above; its numbered steps
                drive a progress bar, with the current step's text under
                it and the full log one tap away. Then it waits until the
                bridge reports the device connected - a recovered device
                that never shows up (its name was released, say) is asked
                for a name after all and registered anew.
  5. Done     - sends the browser to https://<name>.off-the.cloud, where the
                app's own first-run wizard asks for the owner password.

Root, standard library only, unauthenticated by design: it runs only on a
device that has nothing on it yet, and disables itself once the install
has completed (systemd also refuses to start it again once
/etc/otc/.install-complete exists).
"""

import base64
import hashlib
import json
import os
import re
import secrets
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

sys.stdout.reconfigure(line_buffering=True)

CONFIG = {
    "port": int(os.environ.get("OTC_SETUP_PORT", "80")),
    "bridge": os.environ.get("OTC_BRIDGE", "off-the.cloud"),
    "install_url": os.environ.get(
        "OTC_INSTALL_URL",
        "https://raw.githubusercontent.com/alonsovidales/otc/main/scripts/verified-install.sh"),
    # install.sh sources this for the device identity instead of
    # generating its own, so the name reserved on the bridge and the
    # identity the device later presents are the same thing.
    "env_file": "/etc/otc/otc-install.env",
    "state_file": "/var/lib/otc/setup-state.json",
    # The owner password, decrypted, for install.sh (root-only, tmpfs); it
    # shreds it once set.
    "owner_password_file": "/run/otc-setup/owner-password",
    # Issue #178: the optional profile and the face-recognition choice, for
    # install.sh's `otc init-profile` (root-only, tmpfs; shredded once set).
    "profile_file": "/run/otc-setup/profile.json",
    "join_request": "/var/lib/otc/wifi_join_request.json",
    "join_result": "/var/lib/otc/wifi_join_result.json",
    # network_setup.py keeps the hotspot up until this exists.
    "setup_done_marker": "/var/lib/otc/setup-done",
    # Issue #137: the wizard's last state, for setup_ble.py to answer a
    # phone that asks after the wizard itself has gone.
    "final_state_file": "/var/lib/otc/setup-final.json",
    "install_complete_marker": "/etc/otc/.install-complete",
    "install_log": "/var/log/otc/setup-install.log",
    "recovery_mount": "/mnt/otc-recovery-check",
    "wifi_interface": "wlan0",
    # Joining the owner's WiFi takes the hotspot down (one radio, one
    # interface), so the page gets this long to tell the person where to
    # continue before the join actually starts.
    "join_delay_s": 2,
    # network_setup.py scans before the hotspot starts and leaves the
    # result here: scanning while a phone is on the hotspot takes the
    # radio away for seconds and drops the captive-portal sheet.
    "scan_file": "/var/lib/otc/wifi_scan.json",
    # The hotspot's own address (network_setup.py pins it): where the
    # captive-portal probes get redirected.
    "ap_address": "10.42.0.1",
    # How long to wait for the freshly installed service to show up on the
    # bridge before calling it offline (it dials in seconds after start).
    "verify_timeout_s": 120,
    "dry_run": os.environ.get("OTC_SETUP_DRY_RUN") == "1",
    "dry_run_offline": os.environ.get("OTC_SETUP_DRY_RUN_OFFLINE") == "1",
}

NAME_RE = re.compile(r"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
# Issue #135: the bridge's country list, fetched once per run.
COUNTRIES = {}
STEP_RE = re.compile(r"\[otc-install\] \[(\d+)/(\d+)\] (.*)")
ERROR_RE = re.compile(r"\[otc-install\] ERROR: (.*)")
INFO_RE = re.compile(r"\[otc-install\] (.*)")
# install.sh's machine-readable line just before a step or an ERROR line:
# "@step <token> [<args>]" / "@error <token> <args>", args a JSON object of
# strings (see Install._feed).
MARK_RE = re.compile(r"\[otc-install\] @(step|error) ([a-z0-9_]{1,40})(?: (\{.*\}))?\s*$")
# "md0 : active (auto-read-only) raid1 sda[0] sdb[1]" - members are the
# tokens carrying a [slot]; the level and any parenthesised state are not.
MDSTAT_RE = re.compile(r"^(md\d+)\s*:\s*active\b(.*)$")
MEMBER_RE = re.compile(r"^([A-Za-z0-9_/-]+)\[\d+\]")

# BEGIN GENERATED I18N
# Generated by make i18n (i18n/cmd/i18ngen) from i18n/strings, prefixes
# common and wiz: do not edit. Language code -> key -> text, a text being
# a string or a plural's {"one": ..., "other": ...}; shipping languages.
# English has every key, the others their translations: fall back to "en".
I18N = {
    "de": {
        "common.accept": "Annehmen",
        "common.add": "Hinzufügen",
        "common.apply": "Anwenden",
        "common.back": "Zurück",
        "common.cancel": "Abbrechen",
        "common.choose": "Auswählen",
        "common.close": "Schließen",
        "common.connect": "Verbinden",
        "common.connecting": "Wird verbunden…",
        "common.create": "Erstellen",
        "common.decline": "Ablehnen",
        "common.delete": "Löschen",
        "common.deleting": "Wird gelöscht…",
        "common.deselect": "Auswahl aufheben",
        "common.disconnect": "Trennen",
        "common.done": "Fertig",
        "common.download": "Herunterladen",
        "common.loading": "Wird geladen…",
        "common.next": "Weiter",
        "common.ok": "OK",
        "common.open": "Öffnen",
        "common.password": "Passwort",
        "common.pause": "Pausieren",
        "common.quit": "Beenden",
        "common.refresh": "Aktualisieren",
        "common.remove": "Entfernen",
        "common.rename": "Umbenennen",
        "common.resume": "Fortsetzen",
        "common.save": "Speichern",
        "common.search": "Suchen",
        "common.select": "Auswählen",
        "common.send": "Senden",
        "common.settings": "Einstellungen",
        "common.share": "Teilen",
        "common.try_again": "Erneut versuchen",
        "common.update": "Aktualisieren",
        "common.updating": "Wird aktualisiert…",
        "common.upload": "Hochladen",
        "wiz.account.accept_terms": "Ich akzeptiere die \u003cterms\u003eNutzungsbedingungen\u003c/terms\u003e und habe die \u003cprivacy\u003eDatenschutzerklärung\u003c/privacy\u003e gelesen.",
        "wiz.account.checking": "Wird geprüft…",
        "wiz.account.choose_country": "Auswählen…",
        "wiz.account.code_hint": "Öffne auf einem beliebigen Gerät die Kontoseite \u003cb\u003e{bridge}/account\u003c/b\u003e, melde dich an (mit deinem Passwort, Google oder Apple) und tippe auf \u003cb\u003eEinrichtungscode anfordern\u003c/b\u003e.",
        "wiz.account.code_label": "Einrichtungscode",
        "wiz.account.code_placeholder": "zum Beispiel K7PX2M4Q",
        "wiz.account.confirm_hint": "Wir haben einen Link an \u003cb\u003e{email}\u003c/b\u003e gesendet. Öffne ihn – auf diesem Handy oder einem anderen Gerät –, um zu bestätigen, dass die Adresse dir gehört, und komm dann zurück, um fortzufahren. Du findest ihn nicht? Sieh im Spam-Ordner nach.",
        "wiz.account.confirm_title": "3 · Bestätige deine E-Mail-Adresse",
        "wiz.account.confirmed": "Ich habe bestätigt – weiter",
        "wiz.account.continue_apple": "Mit Apple fortfahren",
        "wiz.account.continue_email": "Mit E-Mail fortfahren",
        "wiz.account.continue_google": "Weiter mit Google",
        "wiz.account.countries_failed": "Die Länderliste konnte nicht geladen werden – prüfe, ob das Gerät online ist",
        "wiz.account.country": "Wohnsitzland",
        "wiz.account.create": "Konto erstellen",
        "wiz.account.email": "E-Mail",
        "wiz.account.have_code": "Ich habe einen Einrichtungscode",
        "wiz.account.heading": "3 · Dein Konto bei Off The Cloud",
        "wiz.account.intro": "Ein Konto verknüpft den Namen dieses Geräts mit dir, damit du es von überall erreichen, mit Freunden teilen und es ersetzen kannst, falls es einmal verloren geht.",
        "wiz.account.name": "Vorname",
        "wiz.account.not_confirmed": "Noch nicht bestätigt – öffne den Link in der E-Mail (wir haben sie gerade erneut gesendet).",
        "wiz.account.one_moment": "Einen Moment…",
        "wiz.account.or_email": "oder mit deiner E-Mail-Adresse",
        "wiz.account.password_min": "Passwort (mindestens 8 Zeichen)",
        "wiz.account.provider_hint": "Mit Google oder Apple registriert? Öffne die Kontoseite \u003cb\u003e{bridge}/account\u003c/b\u003e auf einem anderen Gerät, hol dir einen Einrichtungscode und wähle „Ich habe einen Einrichtungscode“.",
        "wiz.account.sign_in": "Anmelden",
        "wiz.account.signed_in_as": "Angemeldet als \u003cb\u003e{email}\u003c/b\u003e. Der Name des Geräts wird für dieses Konto registriert.",
        "wiz.account.signin_cancelled": "Anmeldung abgebrochen",
        "wiz.account.signin_failed": "Anmeldung fehlgeschlagen",
        "wiz.account.skip": "Ohne Konto fortfahren",
        "wiz.account.skip_hint": "Ohne Konto nutzt das Gerät die Bridge nicht. Es funktioniert trotzdem als NAS in deinem Heimnetz: Dateien, Foto-Backup aus den Apps und die Sync-Clients zu Hause, die Fotogalerie, Tags und Personen. Die Bridge brauchst du für: den Zugriff auf das Gerät von außerhalb deines Zuhauses, die sozialen Funktionen (Freunde, Beiträge, Kommentare), Freigabelinks, die von überall funktionieren, und Push-Benachrichtigungen. Du kannst dich später in den Einstellungen der Web-App mit der Bridge verbinden.",
        "wiz.account.surname": "Nachname",
        "wiz.account.terms_bridge": "Ein Konto brauchst du nur, um unsere Bridge zu nutzen: Du bekommst bis zu \u003cb\u003e5 Domains\u003c/b\u003e wie \u003ci\u003ename\u003c/i\u003e.{bridge}, um dich mit deinem Gerät zu verbinden und auch mit den Geräten deiner Freunde und Familie. Wenn du dieses Gerät nur in deinem lokalen Netzwerk als NAS oder mit Tailscale Funnel nutzen möchtest, überspring das Erstellen des Kontos.",
        "wiz.account.terms_price": "Ein Konto für bis zu 5 Domains ist \u003cb\u003ein den ersten zwei Jahren kostenlos\u003c/b\u003e und kostet danach 9,99 Euro zuzüglich MwSt. pro Jahr (wir schreiben dir eine E-Mail, bevor der Zeitraum endet). Wenn du mehr Domains brauchst, kontaktiere uns bitte unter \u003cb\u003einfo@off-the.cloud\u003c/b\u003e.",
        "wiz.account.title": "3 · Dein Konto",
        "wiz.account.use_another": "Anderes Konto verwenden",
        "wiz.account.use_code": "Diesen Code verwenden",
        "wiz.action.continue": "Weiter",
        "wiz.action.continue_failed": "Fortfahren fehlgeschlagen",
        "wiz.bridge.account_check_unavailable": "dein Konto kann gerade nicht geprüft werden",
        "wiz.bridge.claim_unavailable": "dieser Name kann gerade nicht reserviert werden",
        "wiz.bridge.domain_limit": "ein Konto kann bis zu 5 Domains registrieren – für weitere (2 Euro pro Jahr je Domain) schreib an info@off-the.cloud",
        "wiz.bridge.email_taken": "mit dieser E-Mail-Adresse gibt es bereits ein Konto – melde dich stattdessen an",
        "wiz.bridge.invalid_credentials": "falsche E-Mail-Adresse oder falsches Passwort – wenn du dich mit Google oder Apple registriert hast, melde dich stattdessen damit an (auf der Kontoseite, danach mit einem Einrichtungscode)",
        "wiz.bridge.invalid_email": "diese E-Mail-Adresse scheint nicht zu stimmen",
        "wiz.bridge.invalid_name": "dieser Name kann nicht verwendet werden",
        "wiz.bridge.login_required": "melde dich an, um einen Namen zu registrieren",
        "wiz.bridge.name_check_unavailable": "dieser Name kann gerade nicht geprüft werden",
        "wiz.bridge.name_held": "dieser Name wurde vor Kurzem freigegeben und bleibt 30 Tage lang für den bisherigen Besitzer reserviert",
        "wiz.bridge.name_taken": "dieser Name ist bereits vergeben",
        "wiz.bridge.password_too_short": "das Passwort braucht mindestens 8 Zeichen",
        "wiz.bridge.profile_incomplete": "Vorname, Nachname und Wohnsitzland sind erforderlich",
        "wiz.bridge.setup_code_check_unavailable": "dieser Einrichtungscode kann gerade nicht geprüft werden",
        "wiz.bridge.setup_unavailable": "die Einrichtung konnte nicht gestartet werden",
        "wiz.bridge.signin_unavailable": "Anmeldung gerade nicht möglich",
        "wiz.bridge.signup_unavailable": "Registrieren ist gerade nicht möglich",
        "wiz.bridge.terms_not_accepted": "akzeptiere zuerst die Nutzungsbedingungen: Melde dich auf der Kontoseite off-the.cloud/account an und akzeptiere sie dort",
        "wiz.bridge.terms_required": "bitte akzeptiere die Bedingungen",
        "wiz.bridge.too_many_attempts": "zu viele fehlgeschlagene Versuche, versuche es später erneut",
        "wiz.bridge.too_many_claims": "bitte warte einen Moment, bevor du einen anderen Namen versuchst",
        "wiz.bridge.too_many_signups": "zu viele Registrierungen von dieser Adresse, versuche es später erneut",
        "wiz.disks.continue_without": "Ohne weitere Laufwerke fortfahren",
        "wiz.disks.found": "Vorhandener Speicher von Off The Cloud gefunden",
        "wiz.disks.looking": "Angeschlossene Laufwerke werden geprüft…",
        "wiz.disks.mounted": "derzeit eingebunden",
        "wiz.disks.none": "Keine weiteren Laufwerke gefunden. Schließ sie an und tippe auf „Neu suchen“ oder fahre ohne sie fort.",
        "wiz.disks.pick_hint": "Wähle \u003cb\u003ezwei\u003c/b\u003e Laufwerke, um sie als RAID1 zu spiegeln – eines kann ausfallen, ohne dass etwas verloren geht. Wähle keines, um vorerst alles auf der SD-Karte zu behalten.",
        "wiz.disks.pick_second": "Wähle ein zweites Laufwerk für RAID1 oder keines.",
        "wiz.disks.recover": "Dieses Gerät wiederherstellen",
        "wiz.disks.recover_hint": "Wenn dieser Pi einen ausgefallenen ersetzt, stelle ihn wieder her: Alles auf diesen Laufwerken – Fotos, Dateien, sein Name und seine Bridge-Identität – kommt so zurück, wie es war, und es wird kein neuer Name gebraucht.",
        "wiz.disks.rescan": "Neu suchen",
        "wiz.disks.start_failed": "Start fehlgeschlagen",
        "wiz.disks.start_fresh": "Neu beginnen",
        "wiz.disks.title": "2 · Speicher",
        "wiz.disks.wipe_continue": "Beide löschen und fortfahren",
        "wiz.disks.wipe_warning": "⚠ Beide Laufwerke werden gelöscht.",
        "wiz.disks.wipe_warning_storage": "⚠ Beide Laufwerke werden gelöscht – einschließlich des vorhandenen Speichers von Off The Cloud darauf.",
        "wiz.disks.with_database": "mit seiner Datenbank",
        "wiz.done.hint": "Dieses Gerät hat seine Einrichtung abgeschlossen.",
        "wiz.done.open": "{domain} öffnen",
        "wiz.done.open_app": "App öffnen",
        "wiz.done.title": "Bereits eingerichtet",
        "wiz.error.account_chosen": "es wurde ein Konto gewählt",
        "wiz.error.already_set_up": "dieses Gerät ist bereits eingerichtet",
        "wiz.error.choose_network": "wähle ein Netzwerk",
        "wiz.error.claim_failed": "dieser Name konnte nicht reserviert werden",
        "wiz.error.confirm_wipe": "diese Laufwerke enthalten ein vorhandenes Array – bestätige, dass es gelöscht wird",
        "wiz.error.disks_choice": "wähle zwei Laufwerke für RAID1 oder keines",
        "wiz.error.invalid_setup_code": "dieser Einrichtungscode ist ungültig oder abgelaufen",
        "wiz.error.login_required": "melde dich zuerst mit deinem Konto bei Off The Cloud an",
        "wiz.error.name_check_failed": "dieser Name kann gerade nicht geprüft werden – versuche es erneut",
        "wiz.error.name_first": "wähle zuerst den Namen des Geräts",
        "wiz.error.name_rule": "nur Kleinbuchstaben, Ziffern und Bindestriche",
        "wiz.error.needs_password": "{ssid} braucht ein Passwort",
        "wiz.error.no_array": "kein vorhandenes Array zum Wiederherstellen",
        "wiz.error.not_found": "nicht gefunden",
        "wiz.error.offline": "keine Internetverbindung – {bridge} ist nicht erreichbar",
        "wiz.error.only_5ghz": "{ssid} ist ein 5-GHz-Netz: Der Hotspot kann ihm dorthin nicht folgen. Richte das Gerät stattdessen in der App mit „Neues Gerät einrichten“ (Bluetooth) ein oder wähle ein 2,4-GHz-Netz.",
        "wiz.error.password_too_short": "das Passwort muss mindestens 8 Zeichen haben",
        "wiz.error.password_unreadable": "das Passwort konnte nicht gelesen werden – lade die Seite neu und versuche es erneut",
        "wiz.error.picture_unusable": "das Profilbild konnte nicht verwendet werden – wähle ein anderes",
        "wiz.error.rebind_failed": "die Einstellungen des Geräts konnten nicht aktualisiert werden: {detail}",
        "wiz.error.setup_code_unavailable": "dieser Einrichtungscode kann gerade nicht geprüft werden – versuche es erneut",
        "wiz.error.signin_failed": "Anmeldung fehlgeschlagen",
        "wiz.error.unknown_action": "unbekannte Aktion",
        "wiz.error.unreachable": "{bridge} ist nicht erreichbar: {detail}",
        "wiz.install.almost": "5 · Fast geschafft",
        "wiz.install.app_box": "Die Installation dauert etwa 20 Minuten. \u003cb\u003eLass diesen Bildschirm geöffnet\u003c/b\u003e und das Handy in der Nähe des Geräts, um sie hier zu verfolgen; wenn sie fertig ist, bietet die App an, sich mit \u003cb\u003e{domain}\u003c/b\u003e zu verbinden. Du hast ihn geschlossen? Öffne \u003cb\u003eNeues Gerät einrichten\u003c/b\u003e erneut, dann erscheint der Fortschritt wieder.",
        "wiz.install.browser_box": "\u003cb\u003eDu kannst dieses Fenster jetzt schließen und die Verbindung trennen.\u003c/b\u003e Die Installation dauert etwa 20 Minuten. Wenn sie abgeschlossen ist, öffne \u003cb\u003ehttps://{domain}\u003c/b\u003e aus einem beliebigen Netzwerk. Um den Fortschritt zwischendurch zu prüfen, verbinde dich wieder mit dem WLAN „Off The Cloud“, dann erscheint diese Seite wieder.",
        "wiz.install.check_again": "Erneut prüfen",
        "wiz.install.checking_with": "Wird bei {bridge} geprüft",
        "wiz.install.error.array_not_assembled": "{disk} enthält noch ein RAID-Array, das nicht zusammengesetzt werden konnte – Löschen abgelehnt. Um wirklich neu anzufangen, führe aus: mdadm --zero-superblock {disk}",
        "wiz.install.error.download": "{url} konnte nicht heruntergeladen werden",
        "wiz.install.error.download_installer": "das geprüfte Installationsprogramm konnte nicht heruntergeladen werden",
        "wiz.install.error.exit_code": "das Installationsprogramm wurde mit Code {code} beendet",
        "wiz.install.error.install_web": "die Web-App konnte nicht installiert werden",
        "wiz.install.error.log_failed": "das Installationsprotokoll konnte nicht geöffnet werden: {detail}",
        "wiz.install.error.mariadb": "mariadb ist nicht innerhalb von 60 s gestartet – prüfe „systemctl status mariadb“",
        "wiz.install.error.owner_password": "das Passwort des Besitzers konnte nicht festgelegt werden",
        "wiz.install.error.pinned_hash": "{url} stimmt nicht mit seinem festgelegten Hash überein",
        "wiz.install.error.recovery_no_array": "eine Wiederherstellung wurde angefordert, aber aus {disk1}/{disk2} konnte kein vorhandenes RAID1-Array zusammengesetzt werden – es wurde nichts gelöscht. Prüfe, ob beide Laufwerke angeschlossen sind, und versuche es erneut.",
        "wiz.install.error.stage_source": "die Quelle konnte nicht bereitgestellt werden",
        "wiz.install.error.start_failed": "das Installationsprogramm konnte nicht gestartet werden: {detail}",
        "wiz.install.error.tag_list": "die Tag-Liste konnte nicht entpackt werden",
        "wiz.install.error.unpack_web": "die Web-App konnte nicht entpackt werden",
        "wiz.install.failed": "Fehlgeschlagen",
        "wiz.install.hint": "Alles wird heruntergeladen und eingerichtet. Das dauert auf einem Raspberry Pi eine Weile – lass das Gerät eingeschaltet.",
        "wiz.install.log": "Protokoll",
        "wiz.install.offline_hint": "Die Installation ist abgeschlossen, aber {bridge} sieht \u003cb\u003e{domain}\u003c/b\u003e nicht als verbunden. Meist braucht es nur noch einen Moment.",
        "wiz.install.offline_title": "5 · Installiert, aber noch nicht erreichbar",
        "wiz.install.open_anyway": "Trotzdem öffnen",
        "wiz.install.starting": "Wird gestartet…",
        "wiz.install.step.account": "Dienstkonto für otc",
        "wiz.install.step.build": "Binärdatei otc erstellen",
        "wiz.install.step.directories": "Laufzeitverzeichnisse",
        "wiz.install.step.face_models": "Modelle für die Gesichtserkennung (issue #52, insgesamt ~10 MB, nur einmal heruntergeladen)",
        "wiz.install.step.go": "Go {version} ({arch})",
        "wiz.install.step.installed": "Installiert",
        "wiz.install.step.onnx": "ONNX Runtime {version} ({arch})",
        "wiz.install.step.packages": "apt-get update + Basispakete",
        "wiz.install.step.proto": "proto/generated erzeugen (protoc + protoc-gen-go)",
        "wiz.install.step.service": "Datenbank, Konfiguration und der systemd-Dienst",
        "wiz.install.step.source": "Quelle von Release {version} (geprüft)",
        "wiz.install.step.storage": "RAID1-Speicher ({path})",
        "wiz.install.step.tagging_model": "Tagging-Modell RAM++ (~870 MB, nur einmal heruntergeladen)",
        "wiz.install.step.web": "Web-App (Release {version}, geprüft)",
        "wiz.install.title_installing": "5 · Installation",
        "wiz.install.title_recovering": "5 · Wiederherstellung",
        "wiz.install.waiting": "Installiert. Warten, bis sich das Gerät mit der Bridge verbindet…",
        "wiz.install.waiting_as": "Installiert. Warten, bis sich das Gerät als \u003cb\u003e{domain}\u003c/b\u003e mit der Bridge verbindet…",
        "wiz.name.available": "{domain} ist verfügbar",
        "wiz.name.check_failed": "Dieser Name konnte nicht geprüft werden",
        "wiz.name.hint": "Das wird seine Adresse im Internet, für dich und für Freunde. Buchstaben, Ziffern und Bindestriche.",
        "wiz.name.install": "Installieren",
        "wiz.name.install_failed": "Die Installation konnte nicht gestartet werden",
        "wiz.name.label": "Gerätename",
        "wiz.name.local_hint": "Du hast dich entschieden, ohne Konto fortzufahren, daher bekommt das Gerät keine Internetadresse. In deinem Heimnetz antwortet es als \u003cb\u003eotc.local\u003c/b\u003e.",
        "wiz.name.local_title": "4 · Keine Bridge",
        "wiz.name.placeholder": "zum Beispiel casa",
        "wiz.name.rebind_hint": "Das Gerät ist zurück, erreicht die Bridge aber nicht unter \u003cb\u003e{domain}\u003c/b\u003e – dieser Name wurde vielleicht freigegeben. Wähle einen Namen, um es erneut zu registrieren; alles andere ist bereits wiederhergestellt.",
        "wiz.name.rebind_hint_old": "Das Gerät ist zurück, erreicht die Bridge aber nicht unter \u003cb\u003eseinem alten Namen\u003c/b\u003e – dieser Name wurde vielleicht freigegeben. Wähle einen Namen, um es erneut zu registrieren; alles andere ist bereits wiederhergestellt.",
        "wiz.name.rebind_title": "Gib deinem wiederhergestellten Gerät einen Namen",
        "wiz.name.register": "Registrieren",
        "wiz.name.reserve_failed": "Dieser Name konnte nicht reserviert werden",
        "wiz.name.reserving": "Wird reserviert…",
        "wiz.name.someone_else": "{domain} gehört jemand anderem – wähle einen anderen Namen",
        "wiz.name.taken": "{domain} ist bereits vergeben. Wenn es eines deiner Geräte ist, wird jenes Gerät beim Fortfahren von dieser Adresse entfernt, und sie geht an dieses.",
        "wiz.name.title": "4 · Gib deinem Gerät einen Namen",
        "wiz.name.yours_offline": "{domain} gehört einem deiner Geräte (gerade offline). Wenn du fortfährst, wird jenes Gerät von dieser Adresse entfernt, und dieses übernimmt sie.",
        "wiz.name.yours_online": "{domain} wird gerade von einem deiner Geräte verwendet, das jetzt online ist. Wenn du fortfährst, wird jenes Gerät von dieser Adresse entfernt (zu Hause funktioniert es weiter), und dieses übernimmt sie.",
        "wiz.owner.hint": "Mindestens 8 Zeichen. Es verschlüsselt alles auf dem Gerät und kann nicht wiederhergestellt werden – bewahre es an einem sicheren Ort auf. Damit meldest du dich am Gerät an.",
        "wiz.owner.mismatch": "Die beiden Passwörter stimmen nicht überein.",
        "wiz.owner.password_label": "Gerätepasswort",
        "wiz.owner.repeat_label": "Passwort wiederholen",
        "wiz.owner.too_short": "Mindestens 8 Zeichen.",
        "wiz.page.blink": "Licht blinken lassen",
        "wiz.page.blinking": "Blinkt – achte auf das blinkende grüne Licht",
        "wiz.page.device_id": "Das ist Gerät \u003cb\u003e{id}\u003c/b\u003e.",
        "wiz.page.lead": "Lass uns dein Gerät einrichten.",
        "wiz.page.title": "Einrichtung von Off The Cloud",
        "wiz.profile.about_label": "Über dich",
        "wiz.profile.about_placeholder": "Eine Zeile über dich",
        "wiz.profile.drag_hint": "Zieh das Foto, um dein Gesicht mittig auszurichten.",
        "wiz.profile.faces_hint": "Fasst dieselben Personen auf deinen Fotos zusammen, damit du nach Personen suchen kannst. Die Funktion läuft nur auf diesem Gerät, analysiert aber alle Personen auf deinen Fotos, nicht nur dich. Ausgeschaltet, solange du das Kästchen nicht ankreuzt.",
        "wiz.profile.faces_label": "Gesichter auf meinen Fotos erkennen",
        "wiz.profile.name_label": "Dein Name",
        "wiz.profile.name_placeholder": "So sehen dich deine Freunde",
        "wiz.profile.optional": "(optional)",
        "wiz.profile.optional_hint": "All das ist optional – du kannst es später in den Einstellungen festlegen.",
        "wiz.profile.picture_label": "Profilbild",
        "wiz.profile.title": "Dein Profil",
        "wiz.profile.zoom": "Zoom",
        "wiz.ready.back": "Dein Gerät ist zurück, mit allem, was es hatte.",
        "wiz.ready.choose_password": "Wähle unten sein Passwort – die App öffnet es dann sofort.",
        "wiz.ready.enter_password": "Gib unten sein Passwort ein, um es in der App zu öffnen.",
        "wiz.ready.from_now": "Ab jetzt ist es erreichbar unter",
        "wiz.ready.hotspot_off": "Der Hotspot „Off The Cloud“ schaltet sich in einer Minute aus.",
        "wiz.ready.installed": "Alles ist installiert.",
        "wiz.ready.lives_at": "Es ist erreichbar unter \u003cb\u003e{domain}\u003c/b\u003e.",
        "wiz.ready.local_hint": "Alles ist installiert. In deinem Heimnetz antwortet das Gerät unter",
        "wiz.ready.local_open": "Öffne diese Adresse von einem beliebigen Gerät zu Hause – dort wirst du zuerst gebeten, das Besitzerpasswort zu wählen. Soll es später von überall erreichbar sein? Erstelle ein Konto unter {bridge} und führe die Einrichtung erneut aus.",
        "wiz.ready.open_address": "Öffne diese Adresse in deinem Browser – merk sie dir, über sie erreichst du dein Gerät von überall.",
        "wiz.ready.owner_password": "Dort wirst du zuerst gebeten, das Besitzerpasswort zu wählen.",
        "wiz.ready.title": "Bereit 🎉",
        "wiz.wifi.bands_signal": "{bands} GHz · {signal} %",
        "wiz.wifi.choose_app": "Wähle das Netzwerk, das das Gerät nutzen soll – 2,4 oder 5 GHz.",
        "wiz.wifi.choose_hotspot": "Wähle das Netzwerk, das das Gerät nutzen soll. Über diesen Hotspot verbindet es sich mit 2,4 GHz und wechselt nach der Einrichtung zu 5 GHz; ein reines 5-GHz-Netz erfordert „Neues Gerät einrichten“ in der App (Bluetooth).",
        "wiz.wifi.enter_password": "Gib das Passwort für {ssid} ein",
        "wiz.wifi.failed": "Fehlgeschlagen",
        "wiz.wifi.hotspot_back": "Der Hotspot ist wieder da – verbinde dich erneut damit.",
        "wiz.wifi.join_failed": "Verbindung mit diesem Netzwerk fehlgeschlagen – prüfe das Passwort.",
        "wiz.wifi.join_rejected": "Verbindung mit diesem Netzwerk fehlgeschlagen – prüfe das Passwort und versuche es erneut.",
        "wiz.wifi.joining_title": "1 · Verbindung mit {ssid}",
        "wiz.wifi.joining_wait": "Verbindung wird hergestellt und auf das Internet gewartet – bis zu einer Minute.",
        "wiz.wifi.keep_open": "Lass diese Seite geöffnet – es geht von selbst weiter, sobald das Gerät online ist.",
        "wiz.wifi.none": "Noch keine Netzwerke – tippe auf „Erneut suchen“.",
        "wiz.wifi.online": "Das Gerät ist online. Du kannst fortfahren oder dich mit einem anderen Netzwerk verbinden.",
        "wiz.wifi.online_via": "Das Gerät ist über \u003cb\u003e{ssid}\u003c/b\u003e online. Du kannst fortfahren oder dich mit einem anderen Netzwerk verbinden.",
        "wiz.wifi.only_5ghz": "Nur 5 GHz – verbinde dich über die App",
        "wiz.wifi.password_for": "Passwort für {ssid}",
        "wiz.wifi.pick_first": "Wähle zuerst ein Netzwerk",
        "wiz.wifi.reconnect": "\u003cb\u003eWenn die Verbindung abbricht, verbinde dich wieder mit dem WLAN „Off The Cloud“.\u003c/b\u003e Der Hotspot startet für ein paar Sekunden neu, um auf den Kanal deines Netzwerks zu wechseln; die meisten Handys verbinden sich von selbst wieder, aber falls deines das nicht tut, wähle „Off The Cloud“ erneut in deinen WLAN-Einstellungen und komm zu dieser Seite zurück.",
        "wiz.wifi.scan_again": "Erneut suchen",
        "wiz.wifi.scanning": "Wird gesucht…",
        "wiz.wifi.title": "1 · Mit deinem WLAN verbinden",
        "wiz.wifi.wrong_password": "Ein falsches Passwort erscheint hier als Fehler; versuche es einfach erneut."
    },
    "en": {
        "common.accept": "Accept",
        "common.add": "Add",
        "common.apply": "Apply",
        "common.back": "Back",
        "common.cancel": "Cancel",
        "common.choose": "Choose",
        "common.close": "Close",
        "common.connect": "Connect",
        "common.connecting": "Connecting…",
        "common.create": "Create",
        "common.decline": "Decline",
        "common.delete": "Delete",
        "common.deleting": "Deleting…",
        "common.deselect": "Deselect",
        "common.disconnect": "Disconnect",
        "common.done": "Done",
        "common.download": "Download",
        "common.loading": "Loading…",
        "common.next": "Next",
        "common.ok": "OK",
        "common.open": "Open",
        "common.password": "Password",
        "common.pause": "Pause",
        "common.quit": "Quit",
        "common.refresh": "Refresh",
        "common.remove": "Remove",
        "common.rename": "Rename",
        "common.resume": "Resume",
        "common.save": "Save",
        "common.search": "Search",
        "common.select": "Select",
        "common.send": "Send",
        "common.settings": "Settings",
        "common.share": "Share",
        "common.try_again": "Try again",
        "common.update": "Update",
        "common.updating": "Updating…",
        "common.upload": "Upload",
        "wiz.account.accept_terms": "I accept the \u003cterms\u003eterms of use\u003c/terms\u003e and have read the \u003cprivacy\u003eprivacy notice\u003c/privacy\u003e.",
        "wiz.account.checking": "Checking…",
        "wiz.account.choose_country": "Choose…",
        "wiz.account.code_hint": "On any device, open \u003cb\u003e{bridge}/account\u003c/b\u003e, sign in (with your password, Google or Apple) and tap \u003cb\u003eGet a setup code\u003c/b\u003e.",
        "wiz.account.code_label": "Setup code",
        "wiz.account.code_placeholder": "e.g. K7PX2M4Q",
        "wiz.account.confirm_hint": "We sent a link to \u003cb\u003e{email}\u003c/b\u003e. Open it - on this phone or any other device - to confirm the address is yours, then come back and continue. Can't find it? Look in the spam folder.",
        "wiz.account.confirm_title": "3 · Confirm your email",
        "wiz.account.confirmed": "I've confirmed it - continue",
        "wiz.account.continue_apple": "Continue with Apple",
        "wiz.account.continue_email": "Continue with Email",
        "wiz.account.continue_google": "Continue with Google",
        "wiz.account.countries_failed": "Could not load the country list - check the device is online",
        "wiz.account.country": "Country of residence",
        "wiz.account.create": "Create account",
        "wiz.account.email": "Email",
        "wiz.account.have_code": "I have a setup code",
        "wiz.account.heading": "3 · Your Off The Cloud account",
        "wiz.account.intro": "An account links this device's name to you, so you can reach it from anywhere, share with friends and replace it if it is ever lost.",
        "wiz.account.name": "Name",
        "wiz.account.not_confirmed": "Not confirmed yet - open the link in the email (we have just sent it again).",
        "wiz.account.one_moment": "One moment…",
        "wiz.account.or_email": "or with your email",
        "wiz.account.password_min": "Password (8 characters or more)",
        "wiz.account.provider_hint": "Signed up with Google or Apple? Open \u003cb\u003e{bridge}/account\u003c/b\u003e on another device, get a setup code and choose \"I have a setup code\".",
        "wiz.account.sign_in": "Sign in",
        "wiz.account.signed_in_as": "Signed in as \u003cb\u003e{email}\u003c/b\u003e. The device's name will be registered to this account.",
        "wiz.account.signin_cancelled": "Sign-in cancelled",
        "wiz.account.signin_failed": "Could not sign in",
        "wiz.account.skip": "Continue without an account",
        "wiz.account.skip_hint": "Without an account the device does not use the bridge. It still works as a NAS on your home network: files, photo backup from the apps and the sync clients while at home, the photo gallery, tags and people. What needs the bridge: reaching the device from outside your home, the social features (friends, posts, comments), share links that work from anywhere, and push notifications. You can join the bridge later, from Settings in the web app.",
        "wiz.account.surname": "Surname",
        "wiz.account.terms_bridge": "Creating an account is only necessary to use our bridge: you will get up to \u003cb\u003e5 domains\u003c/b\u003e like \u003ci\u003ename\u003c/i\u003e.{bridge} to connect to your device and also connect with the devices of your friends and family. If you only want to use this device in your local network as a NAS or with Tailscale Funnel, skip the creation of the account.",
        "wiz.account.terms_price": "An account for up to 5 domains is \u003cb\u003efree for the first two years\u003c/b\u003e, and after this period will cost 9.99 euros + VAT a year (we will e-mail you before the period ends). If you need more domains, please contact us at \u003cb\u003einfo@off-the.cloud\u003c/b\u003e.",
        "wiz.account.title": "3 · Your account",
        "wiz.account.use_another": "Use another account",
        "wiz.account.use_code": "Use this code",
        "wiz.action.continue": "Continue",
        "wiz.action.continue_failed": "Could not continue",
        "wiz.bridge.account_check_unavailable": "could not check your account right now",
        "wiz.bridge.claim_unavailable": "could not reserve that name right now",
        "wiz.bridge.domain_limit": "an account can register up to 5 domains - for more (2 euros a year each), write to info@off-the.cloud",
        "wiz.bridge.email_taken": "there is already an account with that email - sign in instead",
        "wiz.bridge.invalid_credentials": "wrong email or password - if you signed up with Google or Apple, use that instead (on the account page, then a setup code)",
        "wiz.bridge.invalid_email": "that email address doesn't look right",
        "wiz.bridge.invalid_name": "that name can't be used",
        "wiz.bridge.login_required": "sign in to register a name",
        "wiz.bridge.name_check_unavailable": "could not check that name right now",
        "wiz.bridge.name_held": "that name was released recently and is held for its previous owner for 30 days",
        "wiz.bridge.name_taken": "that name is already taken",
        "wiz.bridge.password_too_short": "the password needs at least 8 characters",
        "wiz.bridge.profile_incomplete": "name, surname and country of residence are required",
        "wiz.bridge.setup_code_check_unavailable": "could not check that setup code right now",
        "wiz.bridge.setup_unavailable": "could not start the setup",
        "wiz.bridge.signin_unavailable": "could not sign in right now",
        "wiz.bridge.signup_unavailable": "could not sign up right now",
        "wiz.bridge.terms_not_accepted": "accept the terms of use first: sign in at off-the.cloud/account and accept them there",
        "wiz.bridge.terms_required": "please accept the terms",
        "wiz.bridge.too_many_attempts": "too many failed attempts, try again later",
        "wiz.bridge.too_many_claims": "please wait a moment before trying another name",
        "wiz.bridge.too_many_signups": "too many sign-ups from this address, try again later",
        "wiz.disks.continue_without": "Continue without extra disks",
        "wiz.disks.found": "Found an existing Off The Cloud storage",
        "wiz.disks.looking": "Looking at the attached disks…",
        "wiz.disks.mounted": "currently mounted",
        "wiz.disks.none": "No extra disks found. Plug them in and tap Rescan, or continue without.",
        "wiz.disks.pick_hint": "Pick \u003cb\u003etwo\u003c/b\u003e disks to mirror them as RAID1 - one can fail without losing anything. Pick none to keep everything on the SD card for now.",
        "wiz.disks.pick_second": "Pick a second disk for RAID1, or none.",
        "wiz.disks.recover": "Recover this device",
        "wiz.disks.recover_hint": "If this Pi is replacing one that died, recover it: everything on these disks - photos, files, its name and its bridge identity - comes back as it was, and no new name is needed.",
        "wiz.disks.rescan": "Rescan",
        "wiz.disks.start_failed": "Could not start",
        "wiz.disks.start_fresh": "Start fresh instead",
        "wiz.disks.title": "2 · Storage",
        "wiz.disks.wipe_continue": "Wipe both and continue",
        "wiz.disks.wipe_warning": "⚠ Both disks will be wiped.",
        "wiz.disks.wipe_warning_storage": "⚠ Both disks will be wiped - including the existing Off The Cloud storage on them.",
        "wiz.disks.with_database": "with its database",
        "wiz.done.hint": "This device has finished its setup.",
        "wiz.done.open": "Open {domain}",
        "wiz.done.open_app": "Open the app",
        "wiz.done.title": "Already set up",
        "wiz.error.account_chosen": "an account was chosen",
        "wiz.error.already_set_up": "this device is already set up",
        "wiz.error.choose_network": "choose a network",
        "wiz.error.claim_failed": "could not reserve that name",
        "wiz.error.confirm_wipe": "those disks hold an existing array - confirm wiping it",
        "wiz.error.disks_choice": "choose two disks for RAID1, or none",
        "wiz.error.invalid_setup_code": "that setup code is not valid or has expired",
        "wiz.error.login_required": "sign in to your Off The Cloud account first",
        "wiz.error.name_check_failed": "could not check that name right now - try again",
        "wiz.error.name_first": "choose the device's name first",
        "wiz.error.name_rule": "lower-case letters, digits and hyphens only",
        "wiz.error.needs_password": "{ssid} needs a password",
        "wiz.error.no_array": "no existing array to recover",
        "wiz.error.not_found": "not found",
        "wiz.error.offline": "no internet connection - {bridge} is not reachable",
        "wiz.error.only_5ghz": "{ssid} is a 5 GHz network: the hotspot can't follow it there. Set the device up with the app's \"Set up a new device\" (Bluetooth) instead, or pick a 2.4 GHz network.",
        "wiz.error.password_too_short": "the password must have 8 characters or more",
        "wiz.error.password_unreadable": "the password could not be read - reload the page and try again",
        "wiz.error.picture_unusable": "the profile picture could not be used - choose another one",
        "wiz.error.rebind_failed": "could not update the device's settings: {detail}",
        "wiz.error.setup_code_unavailable": "could not check that setup code right now - try again",
        "wiz.error.signin_failed": "could not sign in",
        "wiz.error.unknown_action": "unknown action",
        "wiz.error.unreachable": "could not reach {bridge}: {detail}",
        "wiz.install.almost": "5 · Almost there",
        "wiz.install.app_box": "The installation takes about 20 minutes. \u003cb\u003eKeep this screen open\u003c/b\u003e and the phone next to the device to follow it here; when it is done the app offers to connect to \u003cb\u003e{domain}\u003c/b\u003e. Closed it? Open \u003cb\u003eSet up a new device\u003c/b\u003e again and the progress comes back.",
        "wiz.install.browser_box": "\u003cb\u003eYou can close this window and disconnect now.\u003c/b\u003e The installation takes about 20 minutes. When it is complete, open \u003cb\u003ehttps://{domain}\u003c/b\u003e from any network. To check the progress meanwhile, connect to the \"Off The Cloud\" WiFi again and this page comes back.",
        "wiz.install.check_again": "Check again",
        "wiz.install.checking_with": "Checking with {bridge}",
        "wiz.install.error.array_not_assembled": "{disk} still holds a RAID array that could not be assembled - refusing to wipe it. To really start over, run: mdadm --zero-superblock {disk}",
        "wiz.install.error.download": "could not download {url}",
        "wiz.install.error.download_installer": "could not download the verified installer",
        "wiz.install.error.exit_code": "the installer exited with code {code}",
        "wiz.install.error.install_web": "could not install the web app",
        "wiz.install.error.log_failed": "could not open the install log: {detail}",
        "wiz.install.error.mariadb": "mariadb did not come up within 60s — check 'systemctl status mariadb'",
        "wiz.install.error.owner_password": "could not set the owner password",
        "wiz.install.error.pinned_hash": "{url} does not match its pinned hash",
        "wiz.install.error.recovery_no_array": "recovery was asked for, but no existing RAID1 array could be assembled from {disk1}/{disk2} - nothing was wiped. Check both disks are connected and try again.",
        "wiz.install.error.stage_source": "could not stage the source",
        "wiz.install.error.start_failed": "could not start the installer: {detail}",
        "wiz.install.error.tag_list": "could not unpack the tag list",
        "wiz.install.error.unpack_web": "could not unpack the web app",
        "wiz.install.failed": "Failed",
        "wiz.install.hint": "Downloading and setting everything up. This takes a while on a Raspberry Pi - keep the device powered.",
        "wiz.install.log": "Log",
        "wiz.install.offline_hint": "The install finished, but {bridge} doesn't see \u003cb\u003e{domain}\u003c/b\u003e connected. Usually this just needs a moment more.",
        "wiz.install.offline_title": "5 · Installed, but not reachable yet",
        "wiz.install.open_anyway": "Open it anyway",
        "wiz.install.starting": "Starting…",
        "wiz.install.step.account": "otc service account",
        "wiz.install.step.build": "Build the otc binary",
        "wiz.install.step.directories": "Runtime directories",
        "wiz.install.step.face_models": "Face recognition models (issue #52, ~10MB total, only downloaded once)",
        "wiz.install.step.go": "Go {version} ({arch})",
        "wiz.install.step.installed": "Installed",
        "wiz.install.step.onnx": "ONNX Runtime {version} ({arch})",
        "wiz.install.step.packages": "apt-get update + base packages",
        "wiz.install.step.proto": "Generate proto/generated (protoc + protoc-gen-go)",
        "wiz.install.step.service": "Database, config, and the systemd service",
        "wiz.install.step.source": "Source of release {version} (verified)",
        "wiz.install.step.storage": "RAID1 storage ({path})",
        "wiz.install.step.tagging_model": "RAM++ tagging model (~870MB, only downloaded once)",
        "wiz.install.step.web": "Web app (release {version}, verified)",
        "wiz.install.title_installing": "5 · Installing",
        "wiz.install.title_recovering": "5 · Recovering",
        "wiz.install.waiting": "Installed. Waiting for the device to connect to the bridge…",
        "wiz.install.waiting_as": "Installed. Waiting for the device to connect to the bridge as \u003cb\u003e{domain}\u003c/b\u003e…",
        "wiz.name.available": "{domain} is available",
        "wiz.name.check_failed": "Could not check that name",
        "wiz.name.hint": "This becomes its address on the internet, for you and for friends. Letters, digits and hyphens.",
        "wiz.name.install": "Install",
        "wiz.name.install_failed": "Could not start the install",
        "wiz.name.label": "Device name",
        "wiz.name.local_hint": "You chose to continue without an account, so the device gets no internet address. On your home network it answers as \u003cb\u003eotc.local\u003c/b\u003e.",
        "wiz.name.local_title": "4 · No bridge",
        "wiz.name.placeholder": "e.g. casa",
        "wiz.name.rebind_hint": "The device is back, but it isn't reaching the bridge as \u003cb\u003e{domain}\u003c/b\u003e - that name may have been released. Choose a name to register it again; everything else is already recovered.",
        "wiz.name.rebind_hint_old": "The device is back, but it isn't reaching the bridge as \u003cb\u003eits old name\u003c/b\u003e - that name may have been released. Choose a name to register it again; everything else is already recovered.",
        "wiz.name.rebind_title": "Name your recovered device",
        "wiz.name.register": "Register",
        "wiz.name.reserve_failed": "Could not reserve that name",
        "wiz.name.reserving": "Reserving…",
        "wiz.name.someone_else": "{domain} belongs to someone else - choose another name",
        "wiz.name.taken": "{domain} is already taken. If it is one of your devices, continuing removes that device from this address and gives it to this one.",
        "wiz.name.title": "4 · Name your device",
        "wiz.name.yours_offline": "{domain} belongs to one of your devices (offline now). If you continue, that device is removed from this address and this one takes it.",
        "wiz.name.yours_online": "{domain} is in use by one of your devices, online right now. If you continue, that device is removed from this address (it keeps working at home) and this one takes it.",
        "wiz.owner.hint": "8 characters or more. It encrypts everything on the device and can't be recovered - keep it somewhere safe. You sign in to the device with it.",
        "wiz.owner.mismatch": "The two passwords don't match.",
        "wiz.owner.password_label": "Device password",
        "wiz.owner.repeat_label": "Repeat the password",
        "wiz.owner.too_short": "8 characters or more.",
        "wiz.page.blink": "Blink its light",
        "wiz.page.blinking": "Blinking - look for the flashing green light",
        "wiz.page.device_id": "This is device \u003cb\u003e{id}\u003c/b\u003e.",
        "wiz.page.lead": "Let's set up your device.",
        "wiz.page.title": "Off The Cloud setup",
        "wiz.profile.about_label": "About you",
        "wiz.profile.about_placeholder": "A line about you",
        "wiz.profile.drag_hint": "Drag the photo to centre your face.",
        "wiz.profile.faces_hint": "Groups the same people across your photos so you can search by person. It runs only on this device, but it looks at everyone in your photos, not just you. Off unless you tick it.",
        "wiz.profile.faces_label": "Recognise faces in my photos",
        "wiz.profile.name_label": "Your name",
        "wiz.profile.name_placeholder": "How your friends see you",
        "wiz.profile.optional": "(optional)",
        "wiz.profile.optional_hint": "All of this is optional - you can set it later in Settings.",
        "wiz.profile.picture_label": "Profile picture",
        "wiz.profile.title": "Your profile",
        "wiz.profile.zoom": "Zoom",
        "wiz.ready.back": "Your device is back, with everything it had.",
        "wiz.ready.choose_password": "Choose its password below - the app then opens it straight away.",
        "wiz.ready.enter_password": "Enter its password below to open it in the app.",
        "wiz.ready.from_now": "From now on it lives at",
        "wiz.ready.hotspot_off": "The \"Off The Cloud\" hotspot switches off in a minute.",
        "wiz.ready.installed": "Everything is installed.",
        "wiz.ready.lives_at": "It lives at \u003cb\u003e{domain}\u003c/b\u003e.",
        "wiz.ready.local_hint": "Everything is installed. On your home network the device answers at",
        "wiz.ready.local_open": "Open that address from any device at home - it will ask you to choose the owner password first. Want it reachable from anywhere later? Create an account at {bridge} and run the setup again.",
        "wiz.ready.open_address": "Open that address in your browser - remember it, it is how you reach your device from anywhere.",
        "wiz.ready.owner_password": "It will ask you to choose the owner password first.",
        "wiz.ready.title": "Ready 🎉",
        "wiz.wifi.bands_signal": "{bands} GHz · {signal}%",
        "wiz.wifi.choose_app": "Choose the network the device should use - 2.4 or 5 GHz.",
        "wiz.wifi.choose_hotspot": "Choose the network the device should use. Over this hotspot it joins on 2.4 GHz and moves to 5 GHz once set up; a network that is only 5 GHz needs the app's \"Set up a new device\" (Bluetooth).",
        "wiz.wifi.enter_password": "Enter the password for {ssid}",
        "wiz.wifi.failed": "Failed",
        "wiz.wifi.hotspot_back": "The hotspot is back - reconnect to it.",
        "wiz.wifi.join_failed": "Could not join that network - check the password.",
        "wiz.wifi.join_rejected": "Could not join that network - check the password and try again.",
        "wiz.wifi.joining_title": "1 · Joining {ssid}",
        "wiz.wifi.joining_wait": "Connecting and waiting for the internet - up to a minute.",
        "wiz.wifi.keep_open": "Keep this page open - it continues on its own once the device is online.",
        "wiz.wifi.none": "No networks yet - tap Scan.",
        "wiz.wifi.online": "The device is online. You can continue, or join a different network.",
        "wiz.wifi.online_via": "The device is online via \u003cb\u003e{ssid}\u003c/b\u003e. You can continue, or join a different network.",
        "wiz.wifi.only_5ghz": "5 GHz only - use the app to join it",
        "wiz.wifi.password_for": "Password for {ssid}",
        "wiz.wifi.pick_first": "Pick a network first",
        "wiz.wifi.reconnect": "\u003cb\u003eIf you get disconnected, reconnect to the \"Off The Cloud\" WiFi.\u003c/b\u003e The hotspot restarts for a few seconds to switch to your network's channel; most phones rejoin it by themselves, but if yours doesn't, pick \"Off The Cloud\" again in your WiFi settings and come back to this page.",
        "wiz.wifi.scan_again": "Scan again",
        "wiz.wifi.scanning": "Scanning…",
        "wiz.wifi.title": "1 · Connect to your WiFi",
        "wiz.wifi.wrong_password": "A wrong password shows up here as an error; just try again."
    },
    "es": {
        "common.accept": "Aceptar",
        "common.add": "Añadir",
        "common.apply": "Aplicar",
        "common.back": "Atrás",
        "common.cancel": "Cancelar",
        "common.choose": "Elegir",
        "common.close": "Cerrar",
        "common.connect": "Conectar",
        "common.connecting": "Conectando…",
        "common.create": "Crear",
        "common.decline": "Rechazar",
        "common.delete": "Eliminar",
        "common.deleting": "Eliminando…",
        "common.deselect": "Deseleccionar",
        "common.disconnect": "Desconectar",
        "common.done": "Hecho",
        "common.download": "Descargar",
        "common.loading": "Cargando…",
        "common.next": "Siguiente",
        "common.ok": "Aceptar",
        "common.open": "Abrir",
        "common.password": "Contraseña",
        "common.pause": "Pausar",
        "common.quit": "Salir",
        "common.refresh": "Actualizar",
        "common.remove": "Quitar",
        "common.rename": "Renombrar",
        "common.resume": "Reanudar",
        "common.save": "Guardar",
        "common.search": "Buscar",
        "common.select": "Seleccionar",
        "common.send": "Enviar",
        "common.settings": "Ajustes",
        "common.share": "Compartir",
        "common.try_again": "Reintentar",
        "common.update": "Actualizar",
        "common.updating": "Actualizando…",
        "common.upload": "Subir",
        "wiz.account.accept_terms": "Acepto las \u003cterms\u003econdiciones de uso\u003c/terms\u003e y he leído el \u003cprivacy\u003eaviso de privacidad\u003c/privacy\u003e.",
        "wiz.account.checking": "Comprobando…",
        "wiz.account.choose_country": "Elige…",
        "wiz.account.code_hint": "En cualquier dispositivo, abre la página de tu cuenta del puente, \u003cb\u003e{bridge}/account\u003c/b\u003e, inicia sesión (con tu contraseña, Google o Apple) y toca \u003cb\u003eObtener un código de configuración\u003c/b\u003e.",
        "wiz.account.code_label": "Código de configuración",
        "wiz.account.code_placeholder": "p. ej., K7PX2M4Q",
        "wiz.account.confirm_hint": "Hemos enviado un enlace a \u003cb\u003e{email}\u003c/b\u003e. Ábrelo (en este teléfono o en cualquier otro dispositivo) para confirmar que la dirección es tuya; después vuelve y continúa. ¿No lo encuentras? Mira en la carpeta de spam.",
        "wiz.account.confirm_title": "3 · Confirma tu correo electrónico",
        "wiz.account.confirmed": "Ya lo he confirmado: continuar",
        "wiz.account.continue_apple": "Continuar con Apple",
        "wiz.account.continue_email": "Continuar con correo",
        "wiz.account.continue_google": "Continuar con Google",
        "wiz.account.countries_failed": "No se ha podido cargar la lista de países: comprueba que el dispositivo está conectado",
        "wiz.account.country": "País de residencia",
        "wiz.account.create": "Crear cuenta",
        "wiz.account.email": "Correo electrónico",
        "wiz.account.have_code": "Tengo un código de configuración",
        "wiz.account.heading": "3 · Tu cuenta de Off The Cloud",
        "wiz.account.intro": "Una cuenta vincula el nombre de este dispositivo contigo, para que puedas acceder a él desde cualquier lugar, compartir con amigos y sustituirlo si alguna vez lo pierdes.",
        "wiz.account.name": "Nombre",
        "wiz.account.not_confirmed": "Aún no está confirmado: abre el enlace del correo (te lo acabamos de volver a enviar).",
        "wiz.account.one_moment": "Un momento…",
        "wiz.account.or_email": "o con tu correo electrónico",
        "wiz.account.password_min": "Contraseña (8 caracteres o más)",
        "wiz.account.provider_hint": "¿Te registraste con Google o Apple? En otro dispositivo, abre la página de tu cuenta del puente, \u003cb\u003e{bridge}/account\u003c/b\u003e, obtén un código de configuración y elige “Tengo un código de configuración”.",
        "wiz.account.sign_in": "Iniciar sesión",
        "wiz.account.signed_in_as": "Has iniciado sesión como \u003cb\u003e{email}\u003c/b\u003e. El nombre del dispositivo se registrará en esta cuenta.",
        "wiz.account.signin_cancelled": "Inicio de sesión cancelado",
        "wiz.account.signin_failed": "No se ha podido iniciar sesión",
        "wiz.account.skip": "Continuar sin cuenta",
        "wiz.account.skip_hint": "Sin cuenta, el dispositivo no usa el puente. Sigue funcionando como NAS en tu red doméstica: archivos, copia de seguridad de fotos desde las apps y los clientes de sincronización mientras estás en casa, la galería de fotos, las etiquetas y las personas. Lo que necesita el puente: acceder al dispositivo desde fuera de casa, las funciones sociales (amigos, publicaciones, comentarios), los enlaces para compartir que funcionan desde cualquier lugar y las notificaciones push. Puedes unirte al puente más adelante desde Ajustes en la app web.",
        "wiz.account.surname": "Apellidos",
        "wiz.account.terms_bridge": "Crear una cuenta solo es necesario para usar nuestro puente: tendrás hasta \u003cb\u003e5 dominios\u003c/b\u003e como \u003ci\u003enombre\u003c/i\u003e.{bridge} para conectarte a tu dispositivo y también conectar con los dispositivos de tus amigos y tu familia. Si solo quieres usar este dispositivo en tu red local como NAS o con Tailscale Funnel, omite la creación de la cuenta.",
        "wiz.account.terms_price": "Una cuenta para hasta 5 dominios es \u003cb\u003egratuita durante los dos primeros años\u003c/b\u003e y, después de ese periodo, costará 9,99 euros + IVA al año (te enviaremos un correo electrónico antes de que termine el periodo). Si necesitas más dominios, escríbenos a \u003cb\u003einfo@off-the.cloud\u003c/b\u003e.",
        "wiz.account.title": "3 · Tu cuenta",
        "wiz.account.use_another": "Usar otra cuenta",
        "wiz.account.use_code": "Usar este código",
        "wiz.action.continue": "Continuar",
        "wiz.action.continue_failed": "No se ha podido continuar",
        "wiz.bridge.account_check_unavailable": "ahora mismo no se puede comprobar tu cuenta",
        "wiz.bridge.claim_unavailable": "ahora mismo no se puede reservar ese nombre",
        "wiz.bridge.domain_limit": "una cuenta puede registrar hasta 5 dominios; para más (2 euros al año cada uno), escribe a info@off-the.cloud",
        "wiz.bridge.email_taken": "ya hay una cuenta con ese correo electrónico: inicia sesión",
        "wiz.bridge.invalid_credentials": "correo electrónico o contraseña incorrectos; si te registraste con Google o Apple, entra así en la página de la cuenta y usa un código de configuración",
        "wiz.bridge.invalid_email": "esa dirección de correo electrónico no parece correcta",
        "wiz.bridge.invalid_name": "ese nombre no se puede usar",
        "wiz.bridge.login_required": "inicia sesión para registrar un nombre",
        "wiz.bridge.name_check_unavailable": "ahora mismo no se puede comprobar ese nombre",
        "wiz.bridge.name_held": "ese nombre se liberó hace poco y está reservado para su anterior propietario durante 30 días",
        "wiz.bridge.name_taken": "ese nombre ya está en uso",
        "wiz.bridge.password_too_short": "la contraseña necesita al menos 8 caracteres",
        "wiz.bridge.profile_incomplete": "el nombre, los apellidos y el país de residencia son obligatorios",
        "wiz.bridge.setup_code_check_unavailable": "ahora mismo no se puede comprobar ese código de configuración",
        "wiz.bridge.setup_unavailable": "no se ha podido iniciar la configuración",
        "wiz.bridge.signin_unavailable": "ahora mismo no se puede iniciar sesión",
        "wiz.bridge.signup_unavailable": "ahora mismo no se puede crear la cuenta",
        "wiz.bridge.terms_not_accepted": "acepta primero las condiciones de uso: inicia sesión en tu cuenta en off-the.cloud/account y acéptalas allí",
        "wiz.bridge.terms_required": "acepta las condiciones",
        "wiz.bridge.too_many_attempts": "demasiados intentos fallidos; vuelve a intentarlo más tarde",
        "wiz.bridge.too_many_claims": "espera un momento antes de probar otro nombre",
        "wiz.bridge.too_many_signups": "demasiados registros desde esta dirección; vuelve a intentarlo más tarde",
        "wiz.disks.continue_without": "Continuar sin discos adicionales",
        "wiz.disks.found": "Se ha encontrado almacenamiento de Off The Cloud",
        "wiz.disks.looking": "Examinando los discos conectados…",
        "wiz.disks.mounted": "montado actualmente",
        "wiz.disks.none": "No se han encontrado discos adicionales. Conéctalos y toca “Volver a buscar”, o continúa sin ellos.",
        "wiz.disks.pick_hint": "Elige \u003cb\u003edos\u003c/b\u003e discos para ponerlos en espejo como RAID1: uno puede fallar sin que se pierda nada. No elijas ninguno para guardarlo todo en la tarjeta SD por ahora.",
        "wiz.disks.pick_second": "Elige un segundo disco para RAID1, o ninguno.",
        "wiz.disks.recover": "Recuperar este dispositivo",
        "wiz.disks.recover_hint": "Si esta Pi sustituye a otra que dejó de funcionar, recupera el dispositivo: todo lo que hay en estos discos (fotos, archivos, su nombre y su identidad en el puente) vuelve tal como estaba, y no hace falta un nombre nuevo.",
        "wiz.disks.rescan": "Volver a buscar",
        "wiz.disks.start_failed": "No se ha podido iniciar",
        "wiz.disks.start_fresh": "Empezar de cero",
        "wiz.disks.title": "2 · Almacenamiento",
        "wiz.disks.wipe_continue": "Borrar los dos y continuar",
        "wiz.disks.wipe_warning": "⚠ Se borrarán los dos discos.",
        "wiz.disks.wipe_warning_storage": "⚠ Se borrarán los dos discos, incluido el almacenamiento de Off The Cloud que contienen.",
        "wiz.disks.with_database": "con su base de datos",
        "wiz.done.hint": "Este dispositivo ha terminado su configuración.",
        "wiz.done.open": "Abrir {domain}",
        "wiz.done.open_app": "Abrir la app",
        "wiz.done.title": "Ya está configurado",
        "wiz.error.account_chosen": "se ha elegido una cuenta",
        "wiz.error.already_set_up": "este dispositivo ya está configurado",
        "wiz.error.choose_network": "elige una red",
        "wiz.error.claim_failed": "no se ha podido reservar ese nombre",
        "wiz.error.confirm_wipe": "esos discos contienen un conjunto RAID existente: confirma que quieres borrarlo",
        "wiz.error.disks_choice": "elige dos discos para RAID1, o ninguno",
        "wiz.error.invalid_setup_code": "ese código de configuración no es válido o ha caducado",
        "wiz.error.login_required": "inicia sesión primero en tu cuenta de Off The Cloud",
        "wiz.error.name_check_failed": "ahora mismo no se puede comprobar ese nombre: vuelve a intentarlo",
        "wiz.error.name_first": "elige primero el nombre del dispositivo",
        "wiz.error.name_rule": "solo letras minúsculas, números y guiones",
        "wiz.error.needs_password": "la red {ssid} necesita una contraseña",
        "wiz.error.no_array": "no hay ningún conjunto RAID existente que recuperar",
        "wiz.error.not_found": "no encontrado",
        "wiz.error.offline": "no hay conexión a internet: no se puede contactar con el puente {bridge}",
        "wiz.error.only_5ghz": "la red {ssid} es de 5 GHz: el punto de acceso no puede seguirla. Configura el dispositivo con “Configurar dispositivo nuevo” (Bluetooth) en la app o elige una red de 2,4 GHz.",
        "wiz.error.password_too_short": "la contraseña debe tener 8 caracteres o más",
        "wiz.error.password_unreadable": "no se ha podido leer la contraseña: recarga la página y vuelve a intentarlo",
        "wiz.error.picture_unusable": "no se ha podido usar la foto de perfil: elige otra",
        "wiz.error.rebind_failed": "no se han podido actualizar los ajustes del dispositivo: {detail}",
        "wiz.error.setup_code_unavailable": "ahora mismo no se puede comprobar ese código de configuración: vuelve a intentarlo",
        "wiz.error.signin_failed": "no se ha podido iniciar sesión",
        "wiz.error.unknown_action": "acción desconocida",
        "wiz.error.unreachable": "no se ha podido contactar con el puente {bridge}: {detail}",
        "wiz.install.almost": "5 · Ya casi está",
        "wiz.install.app_box": "La instalación tarda unos 20 minutos. \u003cb\u003eMantén esta pantalla abierta\u003c/b\u003e y el teléfono cerca del dispositivo para seguirla aquí; cuando termine, la app te ofrecerá conectarte a \u003cb\u003e{domain}\u003c/b\u003e. ¿La has cerrado? Vuelve a abrir \u003cb\u003eConfigurar dispositivo nuevo\u003c/b\u003e y el progreso volverá a aparecer.",
        "wiz.install.browser_box": "\u003cb\u003eYa puedes cerrar esta ventana y desconectarte.\u003c/b\u003e La instalación tarda unos 20 minutos. Cuando termine, abre \u003cb\u003ehttps://{domain}\u003c/b\u003e desde cualquier red. Para ver el progreso mientras tanto, vuelve a conectarte a la red Wi-Fi “Off The Cloud” y esta página volverá a aparecer.",
        "wiz.install.check_again": "Volver a comprobar",
        "wiz.install.checking_with": "Comprobando con el puente {bridge}",
        "wiz.install.error.array_not_assembled": "{disk} aún contiene un conjunto RAID que no se ha podido ensamblar: no se va a borrar. Para empezar de cero de verdad, ejecuta: mdadm --zero-superblock {disk}",
        "wiz.install.error.download": "no se ha podido descargar {url}",
        "wiz.install.error.download_installer": "no se ha podido descargar el instalador verificado",
        "wiz.install.error.exit_code": "el instalador ha terminado con el código {code}",
        "wiz.install.error.install_web": "no se ha podido instalar la app web",
        "wiz.install.error.log_failed": "no se ha podido abrir el registro de instalación: {detail}",
        "wiz.install.error.mariadb": "mariadb no se ha iniciado en 60 s: comprueba “systemctl status mariadb”",
        "wiz.install.error.owner_password": "no se ha podido establecer la contraseña del propietario",
        "wiz.install.error.pinned_hash": "{url} no coincide con su hash fijado",
        "wiz.install.error.recovery_no_array": "se ha solicitado la recuperación, pero no se ha podido ensamblar ningún conjunto RAID1 existente a partir de {disk1}/{disk2}: no se ha borrado nada. Comprueba que los dos discos están conectados y vuelve a intentarlo.",
        "wiz.install.error.stage_source": "no se ha podido preparar el código fuente",
        "wiz.install.error.start_failed": "no se ha podido iniciar el instalador: {detail}",
        "wiz.install.error.tag_list": "no se ha podido descomprimir la lista de etiquetas",
        "wiz.install.error.unpack_web": "no se ha podido descomprimir la app web",
        "wiz.install.failed": "Ha fallado",
        "wiz.install.hint": "Descargando y configurándolo todo. Esto tarda un rato en una Raspberry Pi: mantén el dispositivo encendido.",
        "wiz.install.log": "Registro",
        "wiz.install.offline_hint": "La instalación ha terminado, pero el puente {bridge} no ve \u003cb\u003e{domain}\u003c/b\u003e conectado. Normalmente solo hace falta un momento más.",
        "wiz.install.offline_title": "5 · Instalado, pero aún no accesible",
        "wiz.install.open_anyway": "Abrirlo de todos modos",
        "wiz.install.starting": "Iniciando…",
        "wiz.install.step.account": "Cuenta de servicio otc",
        "wiz.install.step.build": "Compilar el binario otc",
        "wiz.install.step.directories": "Carpetas de ejecución",
        "wiz.install.step.face_models": "Modelos de reconocimiento facial (issue #52, ~10 MB en total, solo se descargan una vez)",
        "wiz.install.step.go": "Go {version} ({arch})",
        "wiz.install.step.installed": "Instalado",
        "wiz.install.step.onnx": "ONNX Runtime {version} ({arch})",
        "wiz.install.step.packages": "apt-get update + paquetes básicos",
        "wiz.install.step.proto": "Generar proto/generated (protoc + protoc-gen-go)",
        "wiz.install.step.service": "Base de datos, configuración y el servicio systemd",
        "wiz.install.step.source": "Código fuente de la versión {version} (verificado)",
        "wiz.install.step.storage": "Almacenamiento RAID1 ({path})",
        "wiz.install.step.tagging_model": "Modelo de etiquetado RAM++ (~870 MB, solo se descarga una vez)",
        "wiz.install.step.web": "App web (versión {version}, verificada)",
        "wiz.install.title_installing": "5 · Instalando",
        "wiz.install.title_recovering": "5 · Recuperando",
        "wiz.install.waiting": "Instalado. Esperando a que el dispositivo se conecte al puente…",
        "wiz.install.waiting_as": "Instalado. Esperando a que el dispositivo se conecte al puente como \u003cb\u003e{domain}\u003c/b\u003e…",
        "wiz.name.available": "{domain} está disponible",
        "wiz.name.check_failed": "No se ha podido comprobar ese nombre",
        "wiz.name.hint": "Será su dirección en internet, para ti y para tus amigos. Letras, números y guiones.",
        "wiz.name.install": "Instalar",
        "wiz.name.install_failed": "No se ha podido iniciar la instalación",
        "wiz.name.label": "Nombre del dispositivo",
        "wiz.name.local_hint": "Has elegido continuar sin cuenta, así que el dispositivo no tiene dirección en internet. En tu red doméstica responde como \u003cb\u003eotc.local\u003c/b\u003e.",
        "wiz.name.local_title": "4 · Sin puente",
        "wiz.name.placeholder": "p. ej., casa",
        "wiz.name.rebind_hint": "El dispositivo ha vuelto, pero no llega al puente como \u003cb\u003e{domain}\u003c/b\u003e: puede que ese nombre se haya liberado. Elige un nombre para volver a registrarlo; todo lo demás ya está recuperado.",
        "wiz.name.rebind_hint_old": "El dispositivo ha vuelto, pero no llega al puente con \u003cb\u003esu nombre anterior\u003c/b\u003e: puede que ese nombre se haya liberado. Elige un nombre para volver a registrarlo; todo lo demás ya está recuperado.",
        "wiz.name.rebind_title": "Ponle nombre a tu dispositivo recuperado",
        "wiz.name.register": "Registrar",
        "wiz.name.reserve_failed": "No se ha podido reservar ese nombre",
        "wiz.name.reserving": "Reservando…",
        "wiz.name.someone_else": "{domain} pertenece a otra persona: elige otro nombre",
        "wiz.name.taken": "{domain} ya está en uso. Si es uno de tus dispositivos, al continuar se le quitará esa dirección a ese dispositivo y pasará a este.",
        "wiz.name.title": "4 · Ponle nombre a tu dispositivo",
        "wiz.name.yours_offline": "{domain} pertenece a uno de tus dispositivos (ahora sin conexión). Si continúas, se le quitará esa dirección a ese dispositivo y pasará a este.",
        "wiz.name.yours_online": "{domain} lo está usando uno de tus dispositivos, conectado ahora mismo. Si continúas, se le quitará esa dirección a ese dispositivo (seguirá funcionando en casa) y pasará a este.",
        "wiz.owner.hint": "8 caracteres o más. Cifra todo lo que hay en el dispositivo y no se puede recuperar: guárdala en un lugar seguro. Con ella inicias sesión en el dispositivo.",
        "wiz.owner.mismatch": "Las dos contraseñas no coinciden.",
        "wiz.owner.password_label": "Contraseña del dispositivo",
        "wiz.owner.repeat_label": "Repite la contraseña",
        "wiz.owner.too_short": "8 caracteres o más.",
        "wiz.page.blink": "Hacer parpadear la luz",
        "wiz.page.blinking": "Parpadeando: busca la luz verde intermitente",
        "wiz.page.device_id": "Este es el dispositivo \u003cb\u003e{id}\u003c/b\u003e.",
        "wiz.page.lead": "Vamos a configurar tu dispositivo.",
        "wiz.page.title": "Configuración de Off The Cloud",
        "wiz.profile.about_label": "Sobre ti",
        "wiz.profile.about_placeholder": "Una frase sobre ti",
        "wiz.profile.drag_hint": "Arrastra la foto para centrar tu cara.",
        "wiz.profile.faces_hint": "Agrupa a las mismas personas en tus fotos para que puedas buscar por persona. Solo se ejecuta en este dispositivo, pero analiza a todas las personas de tus fotos, no solo a ti. Desactivado salvo que lo marques.",
        "wiz.profile.faces_label": "Reconocer caras en mis fotos",
        "wiz.profile.name_label": "Tu nombre",
        "wiz.profile.name_placeholder": "Cómo te ven tus amigos",
        "wiz.profile.optional": "(opcional)",
        "wiz.profile.optional_hint": "Todo esto es opcional: puedes configurarlo más adelante en Ajustes.",
        "wiz.profile.picture_label": "Foto de perfil",
        "wiz.profile.title": "Tu perfil",
        "wiz.profile.zoom": "Zoom",
        "wiz.ready.back": "Tu dispositivo ha vuelto, con todo lo que tenía.",
        "wiz.ready.choose_password": "Elige su contraseña abajo: la app lo abrirá enseguida.",
        "wiz.ready.enter_password": "Introduce su contraseña abajo para abrirlo en la app.",
        "wiz.ready.from_now": "A partir de ahora está en",
        "wiz.ready.hotspot_off": "El punto de acceso “Off The Cloud” se desactivará en un minuto.",
        "wiz.ready.installed": "Todo está instalado.",
        "wiz.ready.lives_at": "Está en \u003cb\u003e{domain}\u003c/b\u003e.",
        "wiz.ready.local_hint": "Todo está instalado. En tu red doméstica, el dispositivo responde en",
        "wiz.ready.local_open": "Abre esa dirección desde cualquier dispositivo de casa: primero te pedirá que elijas la contraseña del propietario. ¿Quieres poder acceder desde cualquier lugar más adelante? Crea una cuenta del puente en {bridge} y vuelve a hacer la configuración.",
        "wiz.ready.open_address": "Abre esa dirección en tu navegador y recuérdala: es como accedes a tu dispositivo desde cualquier lugar.",
        "wiz.ready.owner_password": "Primero te pedirá que elijas la contraseña del propietario.",
        "wiz.ready.title": "Listo 🎉",
        "wiz.wifi.bands_signal": "{bands} GHz · {signal} %",
        "wiz.wifi.choose_app": "Elige la red que debe usar el dispositivo: 2,4 o 5 GHz.",
        "wiz.wifi.choose_hotspot": "Elige la red que debe usar el dispositivo. A través de este punto de acceso se conecta en 2,4 GHz y pasa a 5 GHz una vez configurado; una red que solo es de 5 GHz necesita “Configurar dispositivo nuevo” (Bluetooth) en la app.",
        "wiz.wifi.enter_password": "Introduce la contraseña de la red {ssid}",
        "wiz.wifi.failed": "Ha fallado",
        "wiz.wifi.hotspot_back": "El punto de acceso ha vuelto: vuelve a conectarte a él.",
        "wiz.wifi.join_failed": "No se ha podido conectar a esa red: comprueba la contraseña.",
        "wiz.wifi.join_rejected": "No se ha podido conectar a esa red: comprueba la contraseña y vuelve a intentarlo.",
        "wiz.wifi.joining_title": "1 · Conectando a {ssid}",
        "wiz.wifi.joining_wait": "Conectando y esperando a internet: puede tardar hasta un minuto.",
        "wiz.wifi.keep_open": "Mantén esta página abierta: continuará sola cuando el dispositivo esté en línea.",
        "wiz.wifi.none": "Aún no hay redes: toca “Volver a buscar”.",
        "wiz.wifi.online": "El dispositivo está conectado a internet. Puedes continuar o conectarte a otra red.",
        "wiz.wifi.online_via": "El dispositivo está conectado a internet a través de \u003cb\u003e{ssid}\u003c/b\u003e. Puedes continuar o conectarte a otra red.",
        "wiz.wifi.only_5ghz": "Solo 5 GHz: usa la app para conectarte a ella",
        "wiz.wifi.password_for": "Contraseña de {ssid}",
        "wiz.wifi.pick_first": "Primero elige una red",
        "wiz.wifi.reconnect": "\u003cb\u003eSi se corta la conexión, vuelve a conectarte a la red Wi-Fi “Off The Cloud”.\u003c/b\u003e El punto de acceso se reinicia unos segundos para cambiar al canal de tu red; la mayoría de los teléfonos se vuelven a conectar solos, pero si el tuyo no lo hace, vuelve a elegir “Off The Cloud” en los ajustes de Wi-Fi y regresa a esta página.",
        "wiz.wifi.scan_again": "Volver a buscar",
        "wiz.wifi.scanning": "Buscando…",
        "wiz.wifi.title": "1 · Conéctate a tu red Wi-Fi",
        "wiz.wifi.wrong_password": "Si la contraseña es incorrecta, aparecerá aquí un error; simplemente vuelve a intentarlo."
    },
    "fr": {
        "common.accept": "Accepter",
        "common.add": "Ajouter",
        "common.apply": "Appliquer",
        "common.back": "Retour",
        "common.cancel": "Annuler",
        "common.choose": "Choisir",
        "common.close": "Fermer",
        "common.connect": "Se connecter",
        "common.connecting": "Connexion…",
        "common.create": "Créer",
        "common.decline": "Refuser",
        "common.delete": "Supprimer",
        "common.deleting": "Suppression…",
        "common.deselect": "Désélectionner",
        "common.disconnect": "Déconnecter",
        "common.done": "OK",
        "common.download": "Télécharger",
        "common.loading": "Chargement…",
        "common.next": "Suivant",
        "common.ok": "OK",
        "common.open": "Ouvrir",
        "common.password": "Mot de passe",
        "common.pause": "Pause",
        "common.quit": "Quitter",
        "common.refresh": "Actualiser",
        "common.remove": "Retirer",
        "common.rename": "Renommer",
        "common.resume": "Reprendre",
        "common.save": "Enregistrer",
        "common.search": "Rechercher",
        "common.select": "Sélectionner",
        "common.send": "Envoyer",
        "common.settings": "Paramètres",
        "common.share": "Partager",
        "common.try_again": "Réessayer",
        "common.update": "Mettre à jour",
        "common.updating": "Mise à jour…",
        "common.upload": "Envoyer",
        "wiz.account.accept_terms": "J’accepte les \u003cterms\u003econditions d’utilisation\u003c/terms\u003e et j’ai lu la \u003cprivacy\u003epolitique de confidentialité\u003c/privacy\u003e.",
        "wiz.account.checking": "Vérification…",
        "wiz.account.choose_country": "Choisissez…",
        "wiz.account.code_hint": "Sur n’importe quel appareil, ouvrez \u003cb\u003e{bridge}/account\u003c/b\u003e, connectez-vous à votre compte (avec votre mot de passe, Google ou Apple) et touchez \u003cb\u003eObtenir un code de configuration\u003c/b\u003e.",
        "wiz.account.code_label": "Code de configuration",
        "wiz.account.code_placeholder": "Par exemple : K7PX2M4Q",
        "wiz.account.confirm_hint": "Nous avons envoyé un lien à \u003cb\u003e{email}\u003c/b\u003e. Ouvrez-le (sur ce téléphone ou un autre appareil) pour confirmer que l’adresse vous appartient, puis revenez et continuez. Vous ne le trouvez pas ? Regardez dans le dossier des spams.",
        "wiz.account.confirm_title": "3 · Confirmez votre adresse e-mail",
        "wiz.account.confirmed": "Je l’ai confirmée, continuer",
        "wiz.account.continue_apple": "Continuer avec Apple",
        "wiz.account.continue_email": "Continuer avec un e-mail",
        "wiz.account.continue_google": "Continuer avec Google",
        "wiz.account.countries_failed": "Impossible de charger la liste des pays : vérifiez que l’appareil est en ligne",
        "wiz.account.country": "Pays de résidence",
        "wiz.account.create": "Créer un compte",
        "wiz.account.email": "E-mail",
        "wiz.account.have_code": "J’ai un code de configuration",
        "wiz.account.heading": "3 · Votre compte Off The Cloud",
        "wiz.account.intro": "Un compte associe le nom de cet appareil à vous : vous pouvez ainsi le joindre de n’importe où, partager avec vos amis et le remplacer si vous le perdez un jour.",
        "wiz.account.name": "Prénom",
        "wiz.account.not_confirmed": "Pas encore confirmée : ouvrez le lien dans l’e-mail (nous venons de le renvoyer).",
        "wiz.account.one_moment": "Un instant…",
        "wiz.account.or_email": "ou avec votre e-mail",
        "wiz.account.password_min": "Mot de passe (au moins 8 caractères)",
        "wiz.account.provider_hint": "Votre compte a été créé avec Google ou Apple ? Ouvrez \u003cb\u003e{bridge}/account\u003c/b\u003e sur un autre appareil, obtenez un code de configuration et choisissez « J’ai un code de configuration ».",
        "wiz.account.sign_in": "Se connecter",
        "wiz.account.signed_in_as": "Connexion établie avec \u003cb\u003e{email}\u003c/b\u003e. Le nom de l’appareil sera enregistré sur ce compte.",
        "wiz.account.signin_cancelled": "Connexion annulée",
        "wiz.account.signin_failed": "Impossible de se connecter",
        "wiz.account.skip": "Continuer sans compte",
        "wiz.account.skip_hint": "Sans compte, l’appareil n’utilise pas la passerelle. Il fonctionne toujours comme NAS sur votre réseau domestique : fichiers, sauvegarde des photos depuis les apps et les clients de synchronisation à la maison, galerie de photos, étiquettes et personnes. Ce qui nécessite la passerelle : joindre l’appareil depuis l’extérieur, les fonctions sociales (amis, publications, commentaires), les liens de partage qui fonctionnent partout et les notifications push. Vous pourrez rejoindre la passerelle plus tard, depuis les Paramètres de l’app web.",
        "wiz.account.surname": "Nom",
        "wiz.account.terms_bridge": "La création d’un compte n’est nécessaire que pour utiliser notre passerelle : vous obtiendrez jusqu’à \u003cb\u003e5 domaines\u003c/b\u003e comme \u003ci\u003enom\u003c/i\u003e.{bridge} pour vous connecter à votre appareil et aussi aux appareils de vos amis et de votre famille. Si vous voulez seulement utiliser cet appareil sur votre réseau local comme NAS ou avec Tailscale Funnel, passez l’étape de création du compte.",
        "wiz.account.terms_price": "Un compte pour 5 domaines maximum est \u003cb\u003egratuit pendant les deux premières années\u003c/b\u003e, puis coûtera 9,99 euros + TVA par an (nous vous enverrons un e-mail avant la fin de cette période). Si vous avez besoin de plus de domaines, veuillez nous contacter à \u003cb\u003einfo@off-the.cloud\u003c/b\u003e.",
        "wiz.account.title": "3 · Votre compte",
        "wiz.account.use_another": "Utiliser un autre compte",
        "wiz.account.use_code": "Utiliser ce code",
        "wiz.action.continue": "Continuer",
        "wiz.action.continue_failed": "Impossible de continuer",
        "wiz.bridge.account_check_unavailable": "impossible de vérifier votre compte pour le moment",
        "wiz.bridge.claim_unavailable": "impossible de réserver ce nom pour le moment",
        "wiz.bridge.domain_limit": "un compte peut enregistrer jusqu’à 5 domaines : pour en avoir plus (2 euros par an chacun), écrivez à info@off-the.cloud",
        "wiz.bridge.email_taken": "un compte existe déjà avec cette adresse e-mail : connectez-vous plutôt",
        "wiz.bridge.invalid_credentials": "e-mail ou mot de passe incorrect : si votre compte a été créé avec Google ou Apple, utilisez plutôt ce moyen (sur la page du compte, puis avec un code de configuration)",
        "wiz.bridge.invalid_email": "cette adresse e-mail ne semble pas correcte",
        "wiz.bridge.invalid_name": "ce nom ne peut pas être utilisé",
        "wiz.bridge.login_required": "connectez-vous pour enregistrer un nom",
        "wiz.bridge.name_check_unavailable": "impossible de vérifier ce nom pour le moment",
        "wiz.bridge.name_held": "ce nom a été libéré récemment et reste réservé à son ancien propriétaire pendant 30 jours",
        "wiz.bridge.name_taken": "ce nom est déjà pris",
        "wiz.bridge.password_too_short": "le mot de passe doit comporter au moins 8 caractères",
        "wiz.bridge.profile_incomplete": "le prénom, le nom et le pays de résidence sont obligatoires",
        "wiz.bridge.setup_code_check_unavailable": "impossible de vérifier ce code de configuration pour le moment",
        "wiz.bridge.setup_unavailable": "impossible de démarrer la configuration",
        "wiz.bridge.signin_unavailable": "impossible de se connecter pour le moment",
        "wiz.bridge.signup_unavailable": "impossible de s’inscrire pour le moment",
        "wiz.bridge.terms_not_accepted": "acceptez d’abord les conditions d’utilisation : connectez-vous à votre compte sur off-the.cloud/account et acceptez-les sur cette page",
        "wiz.bridge.terms_required": "veuillez accepter les conditions",
        "wiz.bridge.too_many_attempts": "trop de tentatives échouées, réessayez plus tard",
        "wiz.bridge.too_many_claims": "veuillez patienter un instant avant d’essayer un autre nom",
        "wiz.bridge.too_many_signups": "trop d’inscriptions depuis cette adresse, réessayez plus tard",
        "wiz.disks.continue_without": "Continuer sans disque supplémentaire",
        "wiz.disks.found": "Stockage Off The Cloud existant détecté",
        "wiz.disks.looking": "Examen des disques branchés…",
        "wiz.disks.mounted": "actuellement monté",
        "wiz.disks.none": "Aucun disque supplémentaire trouvé. Branchez-les et touchez Réanalyser, ou continuez sans.",
        "wiz.disks.pick_hint": "Choisissez \u003cb\u003edeux\u003c/b\u003e disques pour les mettre en miroir en RAID1 : l’un peut tomber en panne sans aucune perte. N’en choisissez aucun pour tout garder sur la carte SD pour l’instant.",
        "wiz.disks.pick_second": "Choisissez un second disque pour le RAID1, ou aucun.",
        "wiz.disks.recover": "Récupérer cet appareil",
        "wiz.disks.recover_hint": "Si ce Pi remplace un Pi tombé en panne, récupérez-le : tout ce qui se trouve sur ces disques (photos, fichiers, son nom et son identité sur la passerelle) revient comme avant, et aucun nouveau nom n’est nécessaire.",
        "wiz.disks.rescan": "Réanalyser",
        "wiz.disks.start_failed": "Impossible de démarrer",
        "wiz.disks.start_fresh": "Repartir de zéro",
        "wiz.disks.title": "2 · Stockage",
        "wiz.disks.wipe_continue": "Effacer les deux et continuer",
        "wiz.disks.wipe_warning": "⚠ Les deux disques seront effacés.",
        "wiz.disks.wipe_warning_storage": "⚠ Les deux disques seront effacés, y compris le stockage Off The Cloud existant qu’ils contiennent.",
        "wiz.disks.with_database": "avec sa base de données",
        "wiz.done.hint": "Cet appareil a terminé sa configuration.",
        "wiz.done.open": "Ouvrir {domain}",
        "wiz.done.open_app": "Ouvrir l’app",
        "wiz.done.title": "Déjà configuré",
        "wiz.error.account_chosen": "un compte a été choisi",
        "wiz.error.already_set_up": "cet appareil est déjà configuré",
        "wiz.error.choose_network": "choisissez un réseau",
        "wiz.error.claim_failed": "impossible de réserver ce nom",
        "wiz.error.confirm_wipe": "ces disques contiennent un volume RAID existant : confirmez son effacement",
        "wiz.error.disks_choice": "choisissez deux disques pour le RAID1, ou aucun",
        "wiz.error.invalid_setup_code": "ce code de configuration n’est pas valide ou a expiré",
        "wiz.error.login_required": "connectez-vous d’abord à votre compte Off The Cloud",
        "wiz.error.name_check_failed": "impossible de vérifier ce nom pour le moment : réessayez",
        "wiz.error.name_first": "choisissez d’abord le nom de l’appareil",
        "wiz.error.name_rule": "lettres minuscules, chiffres et traits d’union uniquement",
        "wiz.error.needs_password": "le réseau {ssid} nécessite un mot de passe",
        "wiz.error.no_array": "aucun volume RAID existant à récupérer",
        "wiz.error.not_found": "introuvable",
        "wiz.error.offline": "aucune connexion Internet : la passerelle {bridge} est injoignable",
        "wiz.error.only_5ghz": "le réseau {ssid} est en 5 GHz : le point d’accès ne peut pas le suivre sur cette bande. Configurez plutôt l’appareil avec « Configurer un nouvel appareil » (Bluetooth) dans l’app, ou choisissez un réseau 2,4 GHz.",
        "wiz.error.password_too_short": "le mot de passe doit comporter au moins 8 caractères",
        "wiz.error.password_unreadable": "le mot de passe n’a pas pu être lu : rechargez la page et réessayez",
        "wiz.error.picture_unusable": "la photo de profil n’a pas pu être utilisée : choisissez-en une autre",
        "wiz.error.rebind_failed": "impossible de mettre à jour les paramètres de l’appareil : {detail}",
        "wiz.error.setup_code_unavailable": "impossible de vérifier ce code de configuration pour le moment : réessayez",
        "wiz.error.signin_failed": "impossible de se connecter",
        "wiz.error.unknown_action": "action inconnue",
        "wiz.error.unreachable": "impossible de joindre la passerelle {bridge} : {detail}",
        "wiz.install.almost": "5 · Presque terminé",
        "wiz.install.app_box": "L’installation prend environ 20 minutes. \u003cb\u003eGardez cet écran ouvert\u003c/b\u003e et le téléphone près de l’appareil pour la suivre ici ; une fois l’installation terminée, l’app vous propose de vous connecter à \u003cb\u003e{domain}\u003c/b\u003e. Vous avez fermé cet écran ? Ouvrez de nouveau \u003cb\u003eConfigurer un nouvel appareil\u003c/b\u003e et la progression réapparaît.",
        "wiz.install.browser_box": "\u003cb\u003eVous pouvez fermer cette fenêtre et vous déconnecter dès maintenant.\u003c/b\u003e L’installation prend environ 20 minutes. Une fois terminée, ouvrez \u003cb\u003ehttps://{domain}\u003c/b\u003e depuis n’importe quel réseau. Pour suivre la progression en attendant, reconnectez-vous au Wi-Fi « Off The Cloud » et cette page réapparaît.",
        "wiz.install.check_again": "Vérifier à nouveau",
        "wiz.install.checking_with": "Vérification auprès de la passerelle {bridge}",
        "wiz.install.error.array_not_assembled": "le disque {disk} contient encore un volume RAID qui n’a pas pu être assemblé : effacement refusé. Pour vraiment repartir de zéro, exécutez : mdadm --zero-superblock {disk}",
        "wiz.install.error.download": "impossible de télécharger {url}",
        "wiz.install.error.download_installer": "impossible de télécharger le programme d’installation vérifié",
        "wiz.install.error.exit_code": "le programme d’installation s’est terminé avec le code {code}",
        "wiz.install.error.install_web": "impossible d’installer l’app web",
        "wiz.install.error.log_failed": "impossible d’ouvrir le journal d’installation : {detail}",
        "wiz.install.error.mariadb": "mariadb n’a pas démarré dans les 60 s : vérifiez « systemctl status mariadb »",
        "wiz.install.error.owner_password": "impossible de définir le mot de passe du propriétaire",
        "wiz.install.error.pinned_hash": "{url} ne correspond pas à son empreinte de référence",
        "wiz.install.error.recovery_no_array": "une récupération a été demandée, mais aucun volume RAID1 existant n’a pu être assemblé à partir de {disk1}/{disk2} : rien n’a été effacé. Vérifiez que les deux disques sont branchés et réessayez.",
        "wiz.install.error.stage_source": "impossible de mettre en place la source",
        "wiz.install.error.start_failed": "impossible de démarrer le programme d’installation : {detail}",
        "wiz.install.error.tag_list": "impossible de décompresser la liste des étiquettes",
        "wiz.install.error.unpack_web": "impossible de décompresser l’app web",
        "wiz.install.failed": "Échec",
        "wiz.install.hint": "Téléchargement et configuration de tous les éléments. Cela prend un moment sur un Raspberry Pi : laissez l’appareil sous tension.",
        "wiz.install.log": "Journal",
        "wiz.install.offline_hint": "L’installation est terminée, mais la passerelle {bridge} ne voit pas \u003cb\u003e{domain}\u003c/b\u003e connecté. Il suffit généralement d’attendre encore un peu.",
        "wiz.install.offline_title": "5 · Installé, mais pas encore joignable",
        "wiz.install.open_anyway": "L’ouvrir quand même",
        "wiz.install.starting": "Démarrage…",
        "wiz.install.step.account": "Compte de service otc",
        "wiz.install.step.build": "Compilation du binaire otc",
        "wiz.install.step.directories": "Dossiers d’exécution",
        "wiz.install.step.face_models": "Modèles de reconnaissance faciale (issue #52, ~10 Mo au total, téléchargés une seule fois)",
        "wiz.install.step.go": "Go {version} ({arch})",
        "wiz.install.step.installed": "Installé",
        "wiz.install.step.onnx": "ONNX Runtime {version} ({arch})",
        "wiz.install.step.packages": "apt-get update + paquets de base",
        "wiz.install.step.proto": "Génération de proto/generated (protoc + protoc-gen-go)",
        "wiz.install.step.service": "Base de données, configuration et service systemd",
        "wiz.install.step.source": "Source de la version {version} (vérifiée)",
        "wiz.install.step.storage": "Stockage RAID1 ({path})",
        "wiz.install.step.tagging_model": "Modèle d’étiquetage RAM++ (~870 Mo, téléchargé une seule fois)",
        "wiz.install.step.web": "App web (version {version}, vérifiée)",
        "wiz.install.title_installing": "5 · Installation",
        "wiz.install.title_recovering": "5 · Récupération",
        "wiz.install.waiting": "Installé. En attente de la connexion de l’appareil à la passerelle…",
        "wiz.install.waiting_as": "Installé. En attente de la connexion de l’appareil à la passerelle sous le nom \u003cb\u003e{domain}\u003c/b\u003e…",
        "wiz.name.available": "L’adresse {domain} est disponible",
        "wiz.name.check_failed": "Impossible de vérifier ce nom",
        "wiz.name.hint": "Ce nom devient son adresse sur Internet, pour vous et pour vos amis. Lettres, chiffres et traits d’union.",
        "wiz.name.install": "Installer",
        "wiz.name.install_failed": "Impossible de démarrer l’installation",
        "wiz.name.label": "Nom de l’appareil",
        "wiz.name.local_hint": "Vous avez choisi de continuer sans compte : l’appareil n’obtient donc pas d’adresse sur Internet. Sur votre réseau domestique, il répond à l’adresse \u003cb\u003eotc.local\u003c/b\u003e.",
        "wiz.name.local_title": "4 · Sans passerelle",
        "wiz.name.placeholder": "Par exemple : casa",
        "wiz.name.rebind_hint": "L’appareil est de retour, mais il ne joint pas la passerelle sous le nom \u003cb\u003e{domain}\u003c/b\u003e : ce nom a peut-être été libéré. Choisissez un nom pour l’enregistrer de nouveau ; tout le reste est déjà récupéré.",
        "wiz.name.rebind_hint_old": "L’appareil est de retour, mais il ne joint pas la passerelle sous \u003cb\u003eson ancien nom\u003c/b\u003e : ce nom a peut-être été libéré. Choisissez un nom pour l’enregistrer de nouveau ; tout le reste est déjà récupéré.",
        "wiz.name.rebind_title": "Nommez votre appareil récupéré",
        "wiz.name.register": "Enregistrer le nom",
        "wiz.name.reserve_failed": "Impossible de réserver ce nom",
        "wiz.name.reserving": "Réservation…",
        "wiz.name.someone_else": "L’adresse {domain} appartient à quelqu’un d’autre : choisissez un autre nom",
        "wiz.name.taken": "L’adresse {domain} est déjà prise. S’il s’agit de l’un de vos appareils, continuer lui retire cette adresse pour l’attribuer à l’appareil que vous configurez.",
        "wiz.name.title": "4 · Nommez votre appareil",
        "wiz.name.yours_offline": "L’adresse {domain} appartient à l’un de vos appareils (actuellement hors ligne). Si vous continuez, cette adresse lui est retirée et attribuée à l’appareil que vous configurez.",
        "wiz.name.yours_online": "L’adresse {domain} est utilisée par l’un de vos appareils, en ligne en ce moment. Si vous continuez, cette adresse lui est retirée (il continue de fonctionner à la maison) et attribuée à l’appareil que vous configurez.",
        "wiz.owner.hint": "Au moins 8 caractères. Il chiffre tout le contenu de l’appareil et ne peut pas être récupéré : conservez-le en lieu sûr. C’est avec lui que vous vous connectez à l’appareil.",
        "wiz.owner.mismatch": "Les deux mots de passe ne correspondent pas.",
        "wiz.owner.password_label": "Mot de passe de l’appareil",
        "wiz.owner.repeat_label": "Répétez le mot de passe",
        "wiz.owner.too_short": "Au moins 8 caractères.",
        "wiz.page.blink": "Faire clignoter le voyant",
        "wiz.page.blinking": "Clignotement : repérez le voyant vert qui clignote",
        "wiz.page.device_id": "Il s’agit de l’appareil \u003cb\u003e{id}\u003c/b\u003e.",
        "wiz.page.lead": "Configurons votre appareil.",
        "wiz.page.title": "Configuration d’Off The Cloud",
        "wiz.profile.about_label": "À propos de vous",
        "wiz.profile.about_placeholder": "Une phrase sur vous",
        "wiz.profile.drag_hint": "Faites glisser la photo pour centrer votre visage.",
        "wiz.profile.faces_hint": "Regroupe les mêmes personnes sur vos photos pour que vous puissiez rechercher par personne. Cette fonction ne s’exécute que sur cet appareil, mais elle analyse toutes les personnes présentes sur vos photos, pas seulement vous. Désactivée tant que vous ne cochez pas la case.",
        "wiz.profile.faces_label": "Reconnaître les visages sur mes photos",
        "wiz.profile.name_label": "Votre nom",
        "wiz.profile.name_placeholder": "Comment vos amis vous voient",
        "wiz.profile.optional": "(facultatif)",
        "wiz.profile.optional_hint": "Tout ceci est facultatif : vous pourrez le définir plus tard dans les Paramètres.",
        "wiz.profile.picture_label": "Photo de profil",
        "wiz.profile.title": "Votre profil",
        "wiz.profile.zoom": "Zoom",
        "wiz.ready.back": "Votre appareil est de retour, avec tout ce qu’il contenait.",
        "wiz.ready.choose_password": "Choisissez son mot de passe ci-dessous : l’app l’ouvre alors immédiatement.",
        "wiz.ready.enter_password": "Saisissez son mot de passe ci-dessous pour l’ouvrir dans l’app.",
        "wiz.ready.from_now": "Désormais, il se trouve à l’adresse",
        "wiz.ready.hotspot_off": "Le point d’accès « Off The Cloud » se désactivera dans une minute.",
        "wiz.ready.installed": "Tout est installé.",
        "wiz.ready.lives_at": "Il se trouve à l’adresse \u003cb\u003e{domain}\u003c/b\u003e.",
        "wiz.ready.local_hint": "Tout est installé. Sur votre réseau domestique, l’appareil répond à l’adresse",
        "wiz.ready.local_open": "Ouvrez cette adresse depuis n’importe quel appareil chez vous : on vous demandera d’abord de choisir le mot de passe du propriétaire. Vous voulez pouvoir le joindre de partout plus tard ? Créez un compte pour la passerelle sur {bridge} et relancez la configuration.",
        "wiz.ready.open_address": "Ouvrez cette adresse dans votre navigateur et retenez-la : c’est elle qui vous permet de joindre votre appareil de partout.",
        "wiz.ready.owner_password": "Il vous demandera d’abord de choisir le mot de passe du propriétaire.",
        "wiz.ready.title": "Prêt 🎉",
        "wiz.wifi.bands_signal": "{bands} GHz · {signal} %",
        "wiz.wifi.choose_app": "Choisissez le réseau que l’appareil doit utiliser (2,4 ou 5 GHz).",
        "wiz.wifi.choose_hotspot": "Choisissez le réseau que l’appareil doit utiliser. Via ce point d’accès, il se connecte en 2,4 GHz et passe en 5 GHz une fois configuré ; un réseau uniquement en 5 GHz nécessite « Configurer un nouvel appareil » (Bluetooth) dans l’app.",
        "wiz.wifi.enter_password": "Saisissez le mot de passe du réseau {ssid}",
        "wiz.wifi.failed": "Échec",
        "wiz.wifi.hotspot_back": "Le point d’accès est de retour : reconnectez-vous-y.",
        "wiz.wifi.join_failed": "Impossible de rejoindre ce réseau : vérifiez le mot de passe.",
        "wiz.wifi.join_rejected": "Impossible de rejoindre ce réseau : vérifiez le mot de passe et réessayez.",
        "wiz.wifi.joining_title": "1 · Connexion à {ssid}",
        "wiz.wifi.joining_wait": "Connexion et attente de l’accès à Internet : jusqu’à une minute.",
        "wiz.wifi.keep_open": "Gardez cette page ouverte : elle continuera d’elle-même une fois l’appareil en ligne.",
        "wiz.wifi.none": "Aucun réseau pour l’instant : touchez « Rechercher à nouveau ».",
        "wiz.wifi.online": "L’appareil est en ligne. Vous pouvez continuer ou rejoindre un autre réseau.",
        "wiz.wifi.online_via": "L’appareil est en ligne via \u003cb\u003e{ssid}\u003c/b\u003e. Vous pouvez continuer ou rejoindre un autre réseau.",
        "wiz.wifi.only_5ghz": "5 GHz uniquement : utilisez l’app pour le rejoindre",
        "wiz.wifi.password_for": "Mot de passe du réseau {ssid}",
        "wiz.wifi.pick_first": "Choisissez d’abord un réseau",
        "wiz.wifi.reconnect": "\u003cb\u003eSi la connexion est perdue, reconnectez-vous au Wi-Fi « Off The Cloud ».\u003c/b\u003e Le point d’accès redémarre quelques secondes pour passer sur le canal de votre réseau ; la plupart des téléphones s’y reconnectent d’eux-mêmes, mais si ce n’est pas le cas du vôtre, choisissez de nouveau « Off The Cloud » dans les réglages Wi-Fi et revenez sur cette page.",
        "wiz.wifi.scan_again": "Rechercher à nouveau",
        "wiz.wifi.scanning": "Recherche…",
        "wiz.wifi.title": "1 · Connectez-vous à votre Wi-Fi",
        "wiz.wifi.wrong_password": "Un mot de passe incorrect s’affiche ici sous forme d’erreur ; réessayez simplement."
    },
    "it": {
        "common.accept": "Accetta",
        "common.add": "Aggiungi",
        "common.apply": "Applica",
        "common.back": "Indietro",
        "common.cancel": "Annulla",
        "common.choose": "Scegli",
        "common.close": "Chiudi",
        "common.connect": "Connetti",
        "common.connecting": "Connessione…",
        "common.create": "Crea",
        "common.decline": "Rifiuta",
        "common.delete": "Elimina",
        "common.deleting": "Eliminazione…",
        "common.deselect": "Deseleziona",
        "common.disconnect": "Disconnetti",
        "common.done": "Fine",
        "common.download": "Scarica",
        "common.loading": "Caricamento…",
        "common.next": "Avanti",
        "common.ok": "OK",
        "common.open": "Apri",
        "common.password": "Password",
        "common.pause": "Pausa",
        "common.quit": "Esci",
        "common.refresh": "Aggiorna",
        "common.remove": "Rimuovi",
        "common.rename": "Rinomina",
        "common.resume": "Riprendi",
        "common.save": "Salva",
        "common.search": "Cerca",
        "common.select": "Seleziona",
        "common.send": "Invia",
        "common.settings": "Impostazioni",
        "common.share": "Condividi",
        "common.try_again": "Riprova",
        "common.update": "Aggiorna",
        "common.updating": "Aggiornamento…",
        "common.upload": "Carica",
        "wiz.account.accept_terms": "Accetto le \u003cterms\u003econdizioni d’uso\u003c/terms\u003e e ho letto l’\u003cprivacy\u003einformativa sulla privacy\u003c/privacy\u003e.",
        "wiz.account.checking": "Verifica…",
        "wiz.account.choose_country": "Scegli…",
        "wiz.account.code_hint": "Su qualsiasi dispositivo, apri \u003cb\u003e{bridge}/account\u003c/b\u003e, accedi (con la tua password, Google o Apple) e tocca \u003cb\u003eOttieni un codice di configurazione\u003c/b\u003e.",
        "wiz.account.code_label": "Codice di configurazione",
        "wiz.account.code_placeholder": "es. K7PX2M4Q",
        "wiz.account.confirm_hint": "Abbiamo inviato un link a \u003cb\u003e{email}\u003c/b\u003e. Aprilo, su questo telefono o su qualsiasi altro dispositivo, per confermare che l’indirizzo è tuo, poi torna qui e continua. Non lo trovi? Controlla nella cartella spam.",
        "wiz.account.confirm_title": "3 · Conferma la tua email",
        "wiz.account.confirmed": "L’ho confermata, continua",
        "wiz.account.continue_apple": "Continua con Apple",
        "wiz.account.continue_email": "Continua con l’email",
        "wiz.account.continue_google": "Continua con Google",
        "wiz.account.countries_failed": "Impossibile caricare l’elenco dei paesi: controlla che il dispositivo sia online",
        "wiz.account.country": "Paese di residenza",
        "wiz.account.create": "Crea account",
        "wiz.account.email": "Email",
        "wiz.account.have_code": "Ho un codice di configurazione",
        "wiz.account.heading": "3 · Il tuo account Off The Cloud",
        "wiz.account.intro": "Un account collega a te il nome di questo dispositivo, così puoi raggiungerlo da qualsiasi luogo, condividere con gli amici e sostituirlo se dovessi perderlo.",
        "wiz.account.name": "Nome",
        "wiz.account.not_confirmed": "Non ancora confermato: apri il link nell’email (l’abbiamo appena inviato di nuovo).",
        "wiz.account.one_moment": "Un momento…",
        "wiz.account.or_email": "oppure con la tua email",
        "wiz.account.password_min": "Password (almeno 8 caratteri)",
        "wiz.account.provider_hint": "Hai creato l’account con Google o Apple? Apri \u003cb\u003e{bridge}/account\u003c/b\u003e su un altro dispositivo, ottieni un codice di configurazione e scegli “Ho un codice di configurazione”.",
        "wiz.account.sign_in": "Accedi",
        "wiz.account.signed_in_as": "Accesso effettuato come \u003cb\u003e{email}\u003c/b\u003e. Il nome del dispositivo verrà registrato su questo account.",
        "wiz.account.signin_cancelled": "Accesso annullato",
        "wiz.account.signin_failed": "Impossibile accedere",
        "wiz.account.skip": "Continua senza account",
        "wiz.account.skip_hint": "Senza un account il dispositivo non usa il bridge. Funziona comunque come NAS sulla tua rete domestica: file, backup delle foto dalle app e dai client di sincronizzazione quando sei a casa, la galleria di foto, le etichette e le persone. Cosa richiede il bridge: raggiungere il dispositivo da fuori casa, le funzioni social (amici, post, commenti), i link di condivisione che funzionano ovunque e le notifiche push. Puoi unirti al bridge più tardi, da Impostazioni nell’app web.",
        "wiz.account.surname": "Cognome",
        "wiz.account.terms_bridge": "Creare un account serve solo per usare il nostro bridge: avrai fino a \u003cb\u003e5 domini\u003c/b\u003e come \u003ci\u003enome\u003c/i\u003e.{bridge} per connetterti al tuo dispositivo e anche ai dispositivi dei tuoi amici e familiari. Se vuoi usare questo dispositivo solo nella tua rete locale come NAS o con Tailscale Funnel, salta la creazione dell’account.",
        "wiz.account.terms_price": "Un account per un massimo di 5 domini è \u003cb\u003egratuito per i primi due anni\u003c/b\u003e, e dopo questo periodo costerà 9,99 euro + IVA all’anno (ti scriveremo un’email prima della fine del periodo). Se ti servono più domini, contattaci all’indirizzo \u003cb\u003einfo@off-the.cloud\u003c/b\u003e.",
        "wiz.account.title": "3 · Il tuo account",
        "wiz.account.use_another": "Usa un altro account",
        "wiz.account.use_code": "Usa questo codice",
        "wiz.action.continue": "Continua",
        "wiz.action.continue_failed": "Impossibile continuare",
        "wiz.bridge.account_check_unavailable": "impossibile verificare il tuo account in questo momento",
        "wiz.bridge.claim_unavailable": "impossibile riservare quel nome in questo momento",
        "wiz.bridge.domain_limit": "un account può registrare fino a 5 domini: per averne di più (2 euro all’anno ciascuno), scrivi a info@off-the.cloud",
        "wiz.bridge.email_taken": "esiste già un account con questa email: accedi invece di registrarti",
        "wiz.bridge.invalid_credentials": "email o password errata: se hai creato l’account con Google o Apple, usa invece quel metodo (nella pagina dell’account, poi un codice di configurazione)",
        "wiz.bridge.invalid_email": "questo indirizzo email non sembra corretto",
        "wiz.bridge.invalid_name": "quel nome non può essere usato",
        "wiz.bridge.login_required": "accedi per registrare un nome",
        "wiz.bridge.name_check_unavailable": "impossibile verificare quel nome in questo momento",
        "wiz.bridge.name_held": "quel nome è stato rilasciato di recente ed è riservato al suo precedente proprietario per 30 giorni",
        "wiz.bridge.name_taken": "quel nome è già in uso",
        "wiz.bridge.password_too_short": "la password deve avere almeno 8 caratteri",
        "wiz.bridge.profile_incomplete": "nome, cognome e paese di residenza sono obbligatori",
        "wiz.bridge.setup_code_check_unavailable": "impossibile verificare quel codice di configurazione in questo momento",
        "wiz.bridge.setup_unavailable": "impossibile avviare la configurazione",
        "wiz.bridge.signin_unavailable": "impossibile accedere in questo momento",
        "wiz.bridge.signup_unavailable": "impossibile registrarsi in questo momento",
        "wiz.bridge.terms_not_accepted": "prima accetta le condizioni d’uso: accedi su off-the.cloud/account e accettale lì",
        "wiz.bridge.terms_required": "accetta le condizioni",
        "wiz.bridge.too_many_attempts": "troppi tentativi non riusciti, riprova più tardi",
        "wiz.bridge.too_many_claims": "attendi un momento prima di provare un altro nome",
        "wiz.bridge.too_many_signups": "troppe registrazioni da questo indirizzo, riprova più tardi",
        "wiz.disks.continue_without": "Continua senza dischi aggiuntivi",
        "wiz.disks.found": "Trovato un archivio Off The Cloud esistente",
        "wiz.disks.looking": "Analisi dei dischi collegati…",
        "wiz.disks.mounted": "attualmente montato",
        "wiz.disks.none": "Nessun disco aggiuntivo trovato. Collegali e tocca “Ripeti ricerca”, oppure continua senza.",
        "wiz.disks.pick_hint": "Scegli \u003cb\u003edue\u003c/b\u003e dischi per metterli in mirror come RAID1: uno può guastarsi senza perdere nulla. Non sceglierne nessuno per tenere tutto sulla scheda SD per ora.",
        "wiz.disks.pick_second": "Scegli un secondo disco per il RAID1, oppure nessuno.",
        "wiz.disks.recover": "Recupera questo dispositivo",
        "wiz.disks.recover_hint": "Se questo Pi sostituisce uno che si è guastato, recuperalo: tutto ciò che è su questi dischi (foto, file, il suo nome e la sua identità sul bridge) torna com’era, e non serve un nuovo nome.",
        "wiz.disks.rescan": "Ripeti ricerca",
        "wiz.disks.start_failed": "Impossibile avviare",
        "wiz.disks.start_fresh": "Ricomincia da zero",
        "wiz.disks.title": "2 · Archiviazione",
        "wiz.disks.wipe_continue": "Cancella entrambi e continua",
        "wiz.disks.wipe_warning": "⚠ Entrambi i dischi verranno cancellati.",
        "wiz.disks.wipe_warning_storage": "⚠ Entrambi i dischi verranno cancellati, compreso l’archivio Off The Cloud esistente che contengono.",
        "wiz.disks.with_database": "con il suo database",
        "wiz.done.hint": "Questo dispositivo ha completato la configurazione.",
        "wiz.done.open": "Apri {domain}",
        "wiz.done.open_app": "Apri l’app",
        "wiz.done.title": "Già configurato",
        "wiz.error.account_chosen": "è stato scelto un account",
        "wiz.error.already_set_up": "questo dispositivo è già configurato",
        "wiz.error.choose_network": "scegli una rete",
        "wiz.error.claim_failed": "impossibile riservare quel nome",
        "wiz.error.confirm_wipe": "quei dischi contengono un array esistente: conferma di volerlo cancellare",
        "wiz.error.disks_choice": "scegli due dischi per il RAID1, oppure nessuno",
        "wiz.error.invalid_setup_code": "il codice di configurazione non è valido o è scaduto",
        "wiz.error.login_required": "prima accedi al tuo account Off The Cloud",
        "wiz.error.name_check_failed": "impossibile verificare quel nome in questo momento: riprova",
        "wiz.error.name_first": "prima scegli il nome del dispositivo",
        "wiz.error.name_rule": "solo lettere minuscole, cifre e trattini",
        "wiz.error.needs_password": "la rete {ssid} richiede una password",
        "wiz.error.no_array": "nessun array esistente da recuperare",
        "wiz.error.not_found": "non trovato",
        "wiz.error.offline": "nessuna connessione a internet: {bridge} non è raggiungibile",
        "wiz.error.only_5ghz": "La rete {ssid} è a 5 GHz: l’hotspot non può seguirla su quella banda. Configura invece il dispositivo con “Configura un nuovo dispositivo” dell’app (Bluetooth), oppure scegli una rete a 2,4 GHz.",
        "wiz.error.password_too_short": "la password deve avere almeno 8 caratteri",
        "wiz.error.password_unreadable": "impossibile leggere la password: ricarica la pagina e riprova",
        "wiz.error.picture_unusable": "impossibile usare la foto del profilo: scegline un’altra",
        "wiz.error.rebind_failed": "impossibile aggiornare le impostazioni del dispositivo: {detail}",
        "wiz.error.setup_code_unavailable": "impossibile verificare quel codice di configurazione in questo momento: riprova",
        "wiz.error.signin_failed": "impossibile accedere",
        "wiz.error.unknown_action": "azione sconosciuta",
        "wiz.error.unreachable": "impossibile raggiungere {bridge}: {detail}",
        "wiz.install.almost": "5 · Ci siamo quasi",
        "wiz.install.app_box": "L’installazione richiede circa 20 minuti. \u003cb\u003eTieni aperta questa schermata\u003c/b\u003e e il telefono vicino al dispositivo per seguirla qui; al termine l’app propone di connettersi a \u003cb\u003e{domain}\u003c/b\u003e. L’hai chiusa? Apri di nuovo \u003cb\u003eConfigura un nuovo dispositivo\u003c/b\u003e e l’avanzamento ricompare.",
        "wiz.install.browser_box": "\u003cb\u003eOra puoi chiudere questa finestra e disconnetterti.\u003c/b\u003e L’installazione richiede circa 20 minuti. Al termine, apri \u003cb\u003ehttps://{domain}\u003c/b\u003e da qualsiasi rete. Per controllare l’avanzamento nel frattempo, connettiti di nuovo alla rete Wi-Fi “Off The Cloud” e questa pagina ricompare.",
        "wiz.install.check_again": "Controlla di nuovo",
        "wiz.install.checking_with": "Verifica con {bridge}",
        "wiz.install.error.array_not_assembled": "{disk} contiene ancora un array RAID che non è stato possibile assemblare: l’installazione si rifiuta di cancellarlo. Per ricominciare davvero da zero, esegui: mdadm --zero-superblock {disk}",
        "wiz.install.error.download": "impossibile scaricare {url}",
        "wiz.install.error.download_installer": "impossibile scaricare il programma di installazione verificato",
        "wiz.install.error.exit_code": "il programma di installazione è terminato con il codice {code}",
        "wiz.install.error.install_web": "impossibile installare l’app web",
        "wiz.install.error.log_failed": "impossibile aprire il log di installazione: {detail}",
        "wiz.install.error.mariadb": "mariadb non si è avviato entro 60 s: controlla “systemctl status mariadb”",
        "wiz.install.error.owner_password": "impossibile impostare la password del proprietario",
        "wiz.install.error.pinned_hash": "{url} non corrisponde al suo hash prefissato",
        "wiz.install.error.recovery_no_array": "è stato richiesto il recupero, ma non è stato possibile assemblare alcun array RAID1 esistente da {disk1}/{disk2}: non è stato cancellato nulla. Controlla che entrambi i dischi siano collegati e riprova.",
        "wiz.install.error.stage_source": "impossibile preparare i sorgenti",
        "wiz.install.error.start_failed": "impossibile avviare il programma di installazione: {detail}",
        "wiz.install.error.tag_list": "impossibile decomprimere l’elenco delle etichette",
        "wiz.install.error.unpack_web": "impossibile decomprimere l’app web",
        "wiz.install.failed": "Installazione non riuscita",
        "wiz.install.hint": "Download e configurazione di tutto in corso. Su un Raspberry Pi richiede un po’ di tempo: tieni il dispositivo acceso.",
        "wiz.install.log": "Log",
        "wiz.install.offline_hint": "L’installazione è terminata, ma {bridge} non vede \u003cb\u003e{domain}\u003c/b\u003e connesso. Di solito basta aspettare ancora un momento.",
        "wiz.install.offline_title": "5 · Installato, ma non ancora raggiungibile",
        "wiz.install.open_anyway": "Aprilo comunque",
        "wiz.install.starting": "Avvio…",
        "wiz.install.step.account": "Account di servizio otc",
        "wiz.install.step.build": "Compilazione dell’eseguibile otc",
        "wiz.install.step.directories": "Cartelle di runtime",
        "wiz.install.step.face_models": "Modelli di riconoscimento facciale (issue #52, ~10 MB in totale, scaricati una sola volta)",
        "wiz.install.step.go": "Go {version} ({arch})",
        "wiz.install.step.installed": "Installato",
        "wiz.install.step.onnx": "ONNX Runtime {version} ({arch})",
        "wiz.install.step.packages": "apt-get update + pacchetti di base",
        "wiz.install.step.proto": "Generazione di proto/generated (protoc + protoc-gen-go)",
        "wiz.install.step.service": "Database, configurazione e servizio systemd",
        "wiz.install.step.source": "Sorgenti della release {version} (verificati)",
        "wiz.install.step.storage": "Archiviazione RAID1 ({path})",
        "wiz.install.step.tagging_model": "Modello di etichettatura RAM++ (~870 MB, scaricato una sola volta)",
        "wiz.install.step.web": "App web (release {version}, verificata)",
        "wiz.install.title_installing": "5 · Installazione",
        "wiz.install.title_recovering": "5 · Recupero",
        "wiz.install.waiting": "Installato. In attesa che il dispositivo si connetta al bridge…",
        "wiz.install.waiting_as": "Installato. In attesa che il dispositivo si connetta al bridge con il nome \u003cb\u003e{domain}\u003c/b\u003e…",
        "wiz.name.available": "{domain} è disponibile",
        "wiz.name.check_failed": "Impossibile verificare quel nome",
        "wiz.name.hint": "Diventa il suo indirizzo su internet, per te e per gli amici. Lettere, cifre e trattini.",
        "wiz.name.install": "Installa",
        "wiz.name.install_failed": "Impossibile avviare l’installazione",
        "wiz.name.label": "Nome del dispositivo",
        "wiz.name.local_hint": "Hai scelto di continuare senza account, quindi il dispositivo non ha un indirizzo su internet. Sulla tua rete domestica risponde come \u003cb\u003eotc.local\u003c/b\u003e.",
        "wiz.name.local_title": "4 · Nessun bridge",
        "wiz.name.placeholder": "es. casa",
        "wiz.name.rebind_hint": "Il dispositivo è tornato, ma non raggiunge il bridge con il nome \u003cb\u003e{domain}\u003c/b\u003e: quel nome potrebbe essere stato rilasciato. Scegli un nome per registrarlo di nuovo; tutto il resto è già stato recuperato.",
        "wiz.name.rebind_hint_old": "Il dispositivo è tornato, ma non raggiunge il bridge con \u003cb\u003eil suo vecchio nome\u003c/b\u003e: quel nome potrebbe essere stato rilasciato. Scegli un nome per registrarlo di nuovo; tutto il resto è già stato recuperato.",
        "wiz.name.rebind_title": "Dai un nome al dispositivo recuperato",
        "wiz.name.register": "Registra",
        "wiz.name.reserve_failed": "Impossibile riservare quel nome",
        "wiz.name.reserving": "Prenotazione…",
        "wiz.name.someone_else": "{domain} appartiene a qualcun altro: scegli un altro nome",
        "wiz.name.taken": "{domain} è già in uso. Se è di uno dei tuoi dispositivi, continuando togli l’indirizzo a quel dispositivo e lo assegni a questo.",
        "wiz.name.title": "4 · Dai un nome al tuo dispositivo",
        "wiz.name.yours_offline": "{domain} appartiene a uno dei tuoi dispositivi (al momento offline). Se continui, l’indirizzo viene tolto a quel dispositivo e passa a questo.",
        "wiz.name.yours_online": "{domain} è usato da uno dei tuoi dispositivi, online in questo momento. Se continui, l’indirizzo viene tolto a quel dispositivo (che continua a funzionare a casa) e passa a questo.",
        "wiz.owner.hint": "Almeno 8 caratteri. Crittografa tutto ciò che è sul dispositivo e non può essere recuperata: conservala in un posto sicuro. La userai per accedere al dispositivo.",
        "wiz.owner.mismatch": "Le due password non corrispondono.",
        "wiz.owner.password_label": "Password del dispositivo",
        "wiz.owner.repeat_label": "Ripeti la password",
        "wiz.owner.too_short": "Almeno 8 caratteri.",
        "wiz.page.blink": "Fai lampeggiare la luce",
        "wiz.page.blinking": "Lampeggia: cerca la luce verde intermittente",
        "wiz.page.device_id": "Questo è il dispositivo \u003cb\u003e{id}\u003c/b\u003e.",
        "wiz.page.lead": "Configuriamo il tuo dispositivo.",
        "wiz.page.title": "Configurazione di Off The Cloud",
        "wiz.profile.about_label": "Su di te",
        "wiz.profile.about_placeholder": "Una riga su di te",
        "wiz.profile.drag_hint": "Trascina la foto per centrare il tuo viso.",
        "wiz.profile.faces_hint": "Raggruppa le stesse persone nelle tue foto, così puoi cercare per persona. Funziona solo su questo dispositivo, ma analizza tutte le persone presenti nelle tue foto, non solo te. Disattivato se non selezioni la casella.",
        "wiz.profile.faces_label": "Riconosci i volti nelle mie foto",
        "wiz.profile.name_label": "Il tuo nome",
        "wiz.profile.name_placeholder": "Come ti vedono i tuoi amici",
        "wiz.profile.optional": "(facoltativo)",
        "wiz.profile.optional_hint": "È tutto facoltativo: puoi impostarlo in seguito in Impostazioni.",
        "wiz.profile.picture_label": "Foto del profilo",
        "wiz.profile.title": "Il tuo profilo",
        "wiz.profile.zoom": "Zoom",
        "wiz.ready.back": "Il tuo dispositivo è tornato, con tutto ciò che aveva.",
        "wiz.ready.choose_password": "Scegli la sua password qui sotto: l’app lo aprirà subito.",
        "wiz.ready.enter_password": "Inserisci la sua password qui sotto per aprirlo nell’app.",
        "wiz.ready.from_now": "D’ora in poi si trova all’indirizzo",
        "wiz.ready.hotspot_off": "L’hotspot “Off The Cloud” si spegne tra un minuto.",
        "wiz.ready.installed": "È tutto installato.",
        "wiz.ready.lives_at": "Si trova all’indirizzo \u003cb\u003e{domain}\u003c/b\u003e.",
        "wiz.ready.local_hint": "È tutto installato. Sulla tua rete domestica il dispositivo risponde all’indirizzo",
        "wiz.ready.local_open": "Apri quell’indirizzo da qualsiasi dispositivo di casa: ti chiederà prima di scegliere la password del proprietario. Vuoi poterlo raggiungere da ovunque in seguito? Crea un account su {bridge} ed esegui di nuovo la configurazione.",
        "wiz.ready.open_address": "Apri quell’indirizzo nel browser e ricordalo: è così che raggiungi il tuo dispositivo da qualsiasi luogo.",
        "wiz.ready.owner_password": "Ti chiederà prima di scegliere la password del proprietario.",
        "wiz.ready.title": "Pronto 🎉",
        "wiz.wifi.bands_signal": "{bands} GHz · {signal}%",
        "wiz.wifi.choose_app": "Scegli la rete che il dispositivo deve usare: 2,4 o 5 GHz.",
        "wiz.wifi.choose_hotspot": "Scegli la rete che il dispositivo deve usare. Tramite questo hotspot si connette a 2,4 GHz e passa a 5 GHz dopo la configurazione; una rete solo a 5 GHz richiede “Configura un nuovo dispositivo” dell’app (Bluetooth).",
        "wiz.wifi.enter_password": "Inserisci la password della rete {ssid}",
        "wiz.wifi.failed": "Operazione non riuscita",
        "wiz.wifi.hotspot_back": "L’hotspot è tornato: riconnettiti.",
        "wiz.wifi.join_failed": "Impossibile connettersi a quella rete: controlla la password.",
        "wiz.wifi.join_rejected": "Impossibile connettersi a quella rete: controlla la password e riprova.",
        "wiz.wifi.joining_title": "1 · Connessione alla rete {ssid}",
        "wiz.wifi.joining_wait": "Connessione e attesa di internet: fino a un minuto.",
        "wiz.wifi.keep_open": "Tieni aperta questa pagina: proseguirà da sola quando il dispositivo sarà online.",
        "wiz.wifi.none": "Ancora nessuna rete: tocca “Cerca di nuovo”.",
        "wiz.wifi.online": "Il dispositivo è online. Puoi continuare o connetterti a un’altra rete.",
        "wiz.wifi.online_via": "Il dispositivo è online tramite \u003cb\u003e{ssid}\u003c/b\u003e. Puoi continuare o connetterti a un’altra rete.",
        "wiz.wifi.only_5ghz": "Solo 5 GHz: usa l’app per connetterti",
        "wiz.wifi.password_for": "Password della rete {ssid}",
        "wiz.wifi.pick_first": "Prima scegli una rete",
        "wiz.wifi.reconnect": "\u003cb\u003eSe la connessione si interrompe, riconnettiti alla rete Wi-Fi “Off The Cloud”.\u003c/b\u003e L’hotspot si riavvia per qualche secondo per passare al canale della tua rete; la maggior parte dei telefoni si riconnette da sola, ma se il tuo non lo fa, scegli di nuovo “Off The Cloud” nelle impostazioni Wi-Fi e torna a questa pagina.",
        "wiz.wifi.scan_again": "Cerca di nuovo",
        "wiz.wifi.scanning": "Ricerca…",
        "wiz.wifi.title": "1 · Connettiti alla tua rete Wi-Fi",
        "wiz.wifi.wrong_password": "Se la password è errata, qui compare un errore: basta riprovare."
    },
    "nl": {
        "common.accept": "Accepteren",
        "common.add": "Toevoegen",
        "common.apply": "Toepassen",
        "common.back": "Terug",
        "common.cancel": "Annuleren",
        "common.choose": "Kiezen",
        "common.close": "Sluiten",
        "common.connect": "Verbinden",
        "common.connecting": "Verbinden…",
        "common.create": "Maken",
        "common.decline": "Weigeren",
        "common.delete": "Verwijderen",
        "common.deleting": "Verwijderen…",
        "common.deselect": "Deselecteren",
        "common.disconnect": "Loskoppelen",
        "common.done": "Klaar",
        "common.download": "Downloaden",
        "common.loading": "Laden…",
        "common.next": "Volgende",
        "common.ok": "OK",
        "common.open": "Openen",
        "common.password": "Wachtwoord",
        "common.pause": "Pauzeren",
        "common.quit": "Stop",
        "common.refresh": "Vernieuwen",
        "common.remove": "Verwijderen",
        "common.rename": "Naam wijzigen",
        "common.resume": "Hervatten",
        "common.save": "Opslaan",
        "common.search": "Zoeken",
        "common.select": "Selecteren",
        "common.send": "Versturen",
        "common.settings": "Instellingen",
        "common.share": "Delen",
        "common.try_again": "Opnieuw proberen",
        "common.update": "Bijwerken",
        "common.updating": "Bijwerken…",
        "common.upload": "Uploaden",
        "wiz.account.accept_terms": "Ik accepteer de \u003cterms\u003egebruiksvoorwaarden\u003c/terms\u003e en heb de \u003cprivacy\u003eprivacyverklaring\u003c/privacy\u003e gelezen.",
        "wiz.account.checking": "Controleren…",
        "wiz.account.choose_country": "Kies…",
        "wiz.account.code_hint": "Open op een willekeurig apparaat \u003cb\u003e{bridge}/account\u003c/b\u003e, log in (met je wachtwoord, Google of Apple) en tik op \u003cb\u003eConfiguratiecode aanvragen\u003c/b\u003e.",
        "wiz.account.code_label": "Configuratiecode",
        "wiz.account.code_placeholder": "bijv. K7PX2M4Q",
        "wiz.account.confirm_hint": "We hebben een link gestuurd naar \u003cb\u003e{email}\u003c/b\u003e. Open die – op deze telefoon of op een ander apparaat – om te bevestigen dat het adres van jou is, en kom dan terug om verder te gaan. Kun je de e-mail niet vinden? Kijk dan in je spammap.",
        "wiz.account.confirm_title": "3 · Bevestig je e-mailadres",
        "wiz.account.confirmed": "Ik heb het bevestigd, doorgaan",
        "wiz.account.continue_apple": "Doorgaan met Apple",
        "wiz.account.continue_email": "Doorgaan met e-mail",
        "wiz.account.continue_google": "Doorgaan met Google",
        "wiz.account.countries_failed": "Kan de landenlijst niet laden – controleer of het apparaat online is",
        "wiz.account.country": "Woonland",
        "wiz.account.create": "Account aanmaken",
        "wiz.account.email": "E-mailadres",
        "wiz.account.have_code": "Ik heb een configuratiecode",
        "wiz.account.heading": "3 · Je Off The Cloud-account",
        "wiz.account.intro": "Een account koppelt de naam van dit apparaat aan jou, zodat je het overal kunt bereiken, met vrienden kunt delen en het kunt vervangen als het ooit verloren gaat.",
        "wiz.account.name": "Voornaam",
        "wiz.account.not_confirmed": "Nog niet bevestigd – open de link in de e-mail (we hebben hem net opnieuw gestuurd).",
        "wiz.account.one_moment": "Een moment…",
        "wiz.account.or_email": "of met je e-mailadres",
        "wiz.account.password_min": "Wachtwoord (minstens 8 tekens)",
        "wiz.account.provider_hint": "Geregistreerd met Google of Apple? Open \u003cb\u003e{bridge}/account\u003c/b\u003e op een ander apparaat, vraag een configuratiecode aan en kies “Ik heb een configuratiecode”.",
        "wiz.account.sign_in": "Inloggen",
        "wiz.account.signed_in_as": "Ingelogd als \u003cb\u003e{email}\u003c/b\u003e. De naam van het apparaat wordt op dit account geregistreerd.",
        "wiz.account.signin_cancelled": "Inloggen geannuleerd",
        "wiz.account.signin_failed": "Kan niet inloggen",
        "wiz.account.skip": "Doorgaan zonder account",
        "wiz.account.skip_hint": "Zonder account gebruikt het apparaat de bridge niet. Het werkt nog steeds als NAS op je thuisnetwerk: bestanden, reservekopieën van foto’s vanuit de apps en de synchronisatieprogramma’s als je thuis bent, de fotogalerij, tags en personen. Waarvoor de bridge nodig is: het apparaat buiten je huis bereiken, de sociale functies (vrienden, berichten, reacties), deellinks die overal werken en pushmeldingen. Je kunt later verbinding maken met de bridge, via Instellingen in de webapp.",
        "wiz.account.surname": "Achternaam",
        "wiz.account.terms_bridge": "Een account is alleen nodig om onze bridge te gebruiken: je krijgt maximaal \u003cb\u003e5 domeinen\u003c/b\u003e zoals \u003ci\u003enaam\u003c/i\u003e.{bridge} om verbinding te maken met je apparaat en ook met de apparaten van je vrienden en familie. Wil je dit apparaat alleen in je lokale netwerk gebruiken als NAS of met Tailscale Funnel? Sla het aanmaken van een account dan over.",
        "wiz.account.terms_price": "Een account voor maximaal 5 domeinen is \u003cb\u003ede eerste twee jaar gratis\u003c/b\u003e en kost daarna € 9,99 + btw per jaar (we sturen je een e-mail voordat die periode afloopt). Heb je meer domeinen nodig? Neem dan contact met ons op via \u003cb\u003einfo@off-the.cloud\u003c/b\u003e.",
        "wiz.account.title": "3 · Je account",
        "wiz.account.use_another": "Ander account gebruiken",
        "wiz.account.use_code": "Deze code gebruiken",
        "wiz.action.continue": "Doorgaan",
        "wiz.action.continue_failed": "Kan niet doorgaan",
        "wiz.bridge.account_check_unavailable": "kan je account nu niet controleren",
        "wiz.bridge.claim_unavailable": "kan die naam nu niet reserveren",
        "wiz.bridge.domain_limit": "een account kan maximaal 5 domeinen registreren – voor meer (2 euro per jaar per domein) schrijf je naar info@off-the.cloud",
        "wiz.bridge.email_taken": "er is al een account met dat e-mailadres – log daarmee in",
        "wiz.bridge.invalid_credentials": "onjuist e-mailadres of wachtwoord – als je je met Google of Apple hebt geregistreerd, gebruik dat dan (via de accountpagina, en daarna een configuratiecode)",
        "wiz.bridge.invalid_email": "dat e-mailadres lijkt niet te kloppen",
        "wiz.bridge.invalid_name": "die naam kan niet worden gebruikt",
        "wiz.bridge.login_required": "log in om een naam te registreren",
        "wiz.bridge.name_check_unavailable": "kan die naam nu niet controleren",
        "wiz.bridge.name_held": "die naam is onlangs vrijgegeven en blijft 30 dagen gereserveerd voor de vorige eigenaar",
        "wiz.bridge.name_taken": "die naam is al in gebruik",
        "wiz.bridge.password_too_short": "het wachtwoord moet minstens 8 tekens hebben",
        "wiz.bridge.profile_incomplete": "voornaam, achternaam en woonland zijn verplicht",
        "wiz.bridge.setup_code_check_unavailable": "kan die configuratiecode nu niet controleren",
        "wiz.bridge.setup_unavailable": "kan de configuratie niet starten",
        "wiz.bridge.signin_unavailable": "kan nu niet inloggen",
        "wiz.bridge.signup_unavailable": "kan het account nu niet aanmaken",
        "wiz.bridge.terms_not_accepted": "accepteer eerst de gebruiksvoorwaarden: log in op off-the.cloud/account en accepteer ze daar",
        "wiz.bridge.terms_required": "accepteer de voorwaarden",
        "wiz.bridge.too_many_attempts": "te veel mislukte pogingen, probeer het later opnieuw",
        "wiz.bridge.too_many_claims": "wacht even voordat je een andere naam probeert",
        "wiz.bridge.too_many_signups": "te veel registraties vanaf dit adres, probeer het later opnieuw",
        "wiz.disks.continue_without": "Doorgaan zonder extra schijven",
        "wiz.disks.found": "Bestaande Off The Cloud-opslag gevonden",
        "wiz.disks.looking": "De aangesloten schijven bekijken…",
        "wiz.disks.mounted": "momenteel gekoppeld",
        "wiz.disks.none": "Geen extra schijven gevonden. Sluit ze aan en tik op Opnieuw zoeken, of ga verder zonder.",
        "wiz.disks.pick_hint": "Kies \u003cb\u003etwee\u003c/b\u003e schijven om ze te spiegelen als RAID1: er kan er één uitvallen zonder dat je iets kwijtraakt. Kies er geen om alles voorlopig op de SD-kaart te houden.",
        "wiz.disks.pick_second": "Kies een tweede schijf voor RAID1, of geen.",
        "wiz.disks.recover": "Dit apparaat herstellen",
        "wiz.disks.recover_hint": "Vervangt deze Pi er een die kapot is gegaan? Herstel hem dan: alles op deze schijven – foto’s, bestanden, de naam en de bridge-identiteit – komt terug zoals het was, en er is geen nieuwe naam nodig.",
        "wiz.disks.rescan": "Opnieuw zoeken",
        "wiz.disks.start_failed": "Kan niet starten",
        "wiz.disks.start_fresh": "Opnieuw beginnen",
        "wiz.disks.title": "2 · Opslag",
        "wiz.disks.wipe_continue": "Beide wissen en doorgaan",
        "wiz.disks.wipe_warning": "⚠ Beide schijven worden gewist.",
        "wiz.disks.wipe_warning_storage": "⚠ Beide schijven worden gewist, inclusief de bestaande Off The Cloud-opslag erop.",
        "wiz.disks.with_database": "met de database",
        "wiz.done.hint": "Dit apparaat heeft de configuratie voltooid.",
        "wiz.done.open": "{domain} openen",
        "wiz.done.open_app": "De app openen",
        "wiz.done.title": "Al ingesteld",
        "wiz.error.account_chosen": "er is een account gekozen",
        "wiz.error.already_set_up": "dit apparaat is al ingesteld",
        "wiz.error.choose_network": "kies een netwerk",
        "wiz.error.claim_failed": "kan die naam niet reserveren",
        "wiz.error.confirm_wipe": "die schijven bevatten een bestaande array – bevestig dat die moet worden gewist",
        "wiz.error.disks_choice": "kies twee schijven voor RAID1, of geen",
        "wiz.error.invalid_setup_code": "die configuratiecode is ongeldig of verlopen",
        "wiz.error.login_required": "log eerst in met je Off The Cloud-account",
        "wiz.error.name_check_failed": "kan die naam nu niet controleren – probeer het opnieuw",
        "wiz.error.name_first": "kies eerst de naam van het apparaat",
        "wiz.error.name_rule": "alleen kleine letters, cijfers en koppeltekens",
        "wiz.error.needs_password": "voor het netwerk {ssid} is een wachtwoord nodig",
        "wiz.error.no_array": "geen bestaande array om te herstellen",
        "wiz.error.not_found": "niet gevonden",
        "wiz.error.offline": "geen internetverbinding – {bridge} is niet bereikbaar",
        "wiz.error.only_5ghz": "het netwerk {ssid} is een 5 GHz-netwerk: de hotspot kan het daar niet volgen. Stel het apparaat in plaats daarvan in met “Nieuw apparaat instellen” (Bluetooth) in de app, of kies een 2,4 GHz-netwerk.",
        "wiz.error.password_too_short": "het wachtwoord moet minstens 8 tekens lang zijn",
        "wiz.error.password_unreadable": "het wachtwoord kan niet worden gelezen – laad de pagina opnieuw en probeer het nog eens",
        "wiz.error.picture_unusable": "de profielfoto kan niet worden gebruikt – kies een andere",
        "wiz.error.rebind_failed": "kan de instellingen van het apparaat niet bijwerken: {detail}",
        "wiz.error.setup_code_unavailable": "kan die configuratiecode nu niet controleren – probeer het opnieuw",
        "wiz.error.signin_failed": "kan niet inloggen",
        "wiz.error.unknown_action": "onbekende actie",
        "wiz.error.unreachable": "kan {bridge} niet bereiken: {detail}",
        "wiz.install.almost": "5 · Bijna klaar",
        "wiz.install.app_box": "De installatie duurt ongeveer 20 minuten. \u003cb\u003eHoud dit scherm open\u003c/b\u003e en de telefoon bij het apparaat om de voortgang hier te volgen; als de installatie klaar is, biedt de app aan om verbinding te maken met \u003cb\u003e{domain}\u003c/b\u003e. Heb je het scherm gesloten? Open \u003cb\u003eNieuw apparaat instellen\u003c/b\u003e opnieuw en de voortgang verschijnt weer.",
        "wiz.install.browser_box": "\u003cb\u003eJe kunt dit venster nu sluiten en de verbinding verbreken.\u003c/b\u003e De installatie duurt ongeveer 20 minuten. Open daarna \u003cb\u003ehttps://{domain}\u003c/b\u003e vanaf elk netwerk. Wil je intussen de voortgang bekijken? Maak dan opnieuw verbinding met de wifi “Off The Cloud” en deze pagina komt terug.",
        "wiz.install.check_again": "Opnieuw controleren",
        "wiz.install.checking_with": "Controleren bij {bridge}",
        "wiz.install.error.array_not_assembled": "{disk} bevat nog een RAID-array die niet kon worden samengesteld – wissen geweigerd. Wil je echt opnieuw beginnen, voer dan uit: mdadm --zero-superblock {disk}",
        "wiz.install.error.download": "kan {url} niet downloaden",
        "wiz.install.error.download_installer": "kan het geverifieerde installatieprogramma niet downloaden",
        "wiz.install.error.exit_code": "het installatieprogramma is gestopt met code {code}",
        "wiz.install.error.install_web": "kan de webapp niet installeren",
        "wiz.install.error.log_failed": "kan het installatielogboek niet openen: {detail}",
        "wiz.install.error.mariadb": "mariadb is niet binnen 60 s gestart: controleer “systemctl status mariadb”",
        "wiz.install.error.owner_password": "kan het wachtwoord van de eigenaar niet instellen",
        "wiz.install.error.pinned_hash": "{url} komt niet overeen met de vastgelegde hash",
        "wiz.install.error.recovery_no_array": "er is om herstel gevraagd, maar er kon geen bestaande RAID1-array worden samengesteld uit {disk1}/{disk2}: er is niets gewist. Controleer of beide schijven zijn aangesloten en probeer het opnieuw.",
        "wiz.install.error.stage_source": "kan de broncode niet klaarzetten",
        "wiz.install.error.start_failed": "kan het installatieprogramma niet starten: {detail}",
        "wiz.install.error.tag_list": "kan de lijst met tags niet uitpakken",
        "wiz.install.error.unpack_web": "kan de webapp niet uitpakken",
        "wiz.install.failed": "Mislukt",
        "wiz.install.hint": "Alles wordt gedownload en ingesteld. Dit duurt even op een Raspberry Pi – houd het apparaat aan de stroom.",
        "wiz.install.log": "Logboek",
        "wiz.install.offline_hint": "De installatie is voltooid, maar {bridge} ziet \u003cb\u003e{domain}\u003c/b\u003e nog niet verbonden. Meestal duurt dit gewoon nog even.",
        "wiz.install.offline_title": "5 · Geïnstalleerd, maar nog niet bereikbaar",
        "wiz.install.open_anyway": "Toch openen",
        "wiz.install.starting": "Starten…",
        "wiz.install.step.account": "otc-serviceaccount",
        "wiz.install.step.build": "otc-binary bouwen",
        "wiz.install.step.directories": "Werkmappen",
        "wiz.install.step.face_models": "Modellen voor gezichtsherkenning (issue #52, ~10 MB totaal, maar één keer gedownload)",
        "wiz.install.step.go": "Go {version} ({arch})",
        "wiz.install.step.installed": "Geïnstalleerd",
        "wiz.install.step.onnx": "ONNX Runtime {version} ({arch})",
        "wiz.install.step.packages": "apt-get update + basispakketten",
        "wiz.install.step.proto": "proto/generated genereren (protoc + protoc-gen-go)",
        "wiz.install.step.service": "Database, configuratie en de systemd-service",
        "wiz.install.step.source": "Broncode van release {version} (geverifieerd)",
        "wiz.install.step.storage": "RAID1-opslag ({path})",
        "wiz.install.step.tagging_model": "RAM++-model voor beeldherkenning (~870 MB, maar één keer gedownload)",
        "wiz.install.step.web": "Webapp (release {version}, geverifieerd)",
        "wiz.install.title_installing": "5 · Installeren",
        "wiz.install.title_recovering": "5 · Herstellen",
        "wiz.install.waiting": "Geïnstalleerd. Wachten tot het apparaat verbinding maakt met de bridge…",
        "wiz.install.waiting_as": "Geïnstalleerd. Wachten tot het apparaat als \u003cb\u003e{domain}\u003c/b\u003e verbinding maakt met de bridge…",
        "wiz.name.available": "{domain} is beschikbaar",
        "wiz.name.check_failed": "Kan die naam niet controleren",
        "wiz.name.hint": "Dit wordt het adres op internet, voor jou en voor vrienden. Letters, cijfers en koppeltekens.",
        "wiz.name.install": "Installeren",
        "wiz.name.install_failed": "Kan de installatie niet starten",
        "wiz.name.label": "Apparaatnaam",
        "wiz.name.local_hint": "Je hebt ervoor gekozen om zonder account verder te gaan, dus het apparaat krijgt geen internetadres. Op je thuisnetwerk is het bereikbaar als \u003cb\u003eotc.local\u003c/b\u003e.",
        "wiz.name.local_title": "4 · Geen bridge",
        "wiz.name.placeholder": "bijv. casa",
        "wiz.name.rebind_hint": "Het apparaat is terug, maar het bereikt de bridge niet als \u003cb\u003e{domain}\u003c/b\u003e: die naam is misschien vrijgegeven. Kies een naam om het opnieuw te registreren; al het andere is al hersteld.",
        "wiz.name.rebind_hint_old": "Het apparaat is terug, maar het bereikt de bridge niet onder \u003cb\u003ezijn oude naam\u003c/b\u003e: die naam is misschien vrijgegeven. Kies een naam om het opnieuw te registreren; al het andere is al hersteld.",
        "wiz.name.rebind_title": "Geef je herstelde apparaat een naam",
        "wiz.name.register": "Registreren",
        "wiz.name.reserve_failed": "Kan die naam niet reserveren",
        "wiz.name.reserving": "Reserveren…",
        "wiz.name.someone_else": "{domain} is van iemand anders – kies een andere naam",
        "wiz.name.taken": "{domain} is al in gebruik. Als het een van je apparaten is, wordt dat apparaat van dit adres verwijderd als je doorgaat, en krijgt dit apparaat het adres.",
        "wiz.name.title": "4 · Geef je apparaat een naam",
        "wiz.name.yours_offline": "{domain} is van een van je apparaten (nu offline). Als je doorgaat, wordt dat apparaat van dit adres verwijderd en neemt dit apparaat het over.",
        "wiz.name.yours_online": "{domain} is in gebruik door een van je apparaten, dat nu online is. Als je doorgaat, wordt dat apparaat van dit adres verwijderd (thuis blijft het werken) en neemt dit apparaat het over.",
        "wiz.owner.hint": "8 tekens of meer. Het versleutelt alles op het apparaat en kan niet worden hersteld – bewaar het op een veilige plek. Je logt ermee in op het apparaat.",
        "wiz.owner.mismatch": "De twee wachtwoorden komen niet overeen.",
        "wiz.owner.password_label": "Apparaatwachtwoord",
        "wiz.owner.repeat_label": "Wachtwoord herhalen",
        "wiz.owner.too_short": "8 tekens of meer.",
        "wiz.page.blink": "Lampje laten knipperen",
        "wiz.page.blinking": "Knippert – zoek het knipperende groene lampje",
        "wiz.page.device_id": "Dit is apparaat \u003cb\u003e{id}\u003c/b\u003e.",
        "wiz.page.lead": "Laten we je apparaat instellen.",
        "wiz.page.title": "Off The Cloud instellen",
        "wiz.profile.about_label": "Over jou",
        "wiz.profile.about_placeholder": "Een regel over jezelf",
        "wiz.profile.drag_hint": "Sleep de foto om je gezicht in het midden te zetten.",
        "wiz.profile.faces_hint": "Groepeert dezelfde personen in je foto’s, zodat je op persoon kunt zoeken. Het werkt alleen op dit apparaat, maar bekijkt iedereen op je foto’s, niet alleen jou. Staat uit, tenzij je het aanvinkt.",
        "wiz.profile.faces_label": "Gezichten herkennen in mijn foto’s",
        "wiz.profile.name_label": "Je naam",
        "wiz.profile.name_placeholder": "Hoe je vrienden je zien",
        "wiz.profile.optional": "(optioneel)",
        "wiz.profile.optional_hint": "Dit is allemaal optioneel – je kunt het later instellen in Instellingen.",
        "wiz.profile.picture_label": "Profielfoto",
        "wiz.profile.title": "Je profiel",
        "wiz.profile.zoom": "Zoomen",
        "wiz.ready.back": "Je apparaat is terug, met alles wat het had.",
        "wiz.ready.choose_password": "Kies hieronder het wachtwoord – de app opent het apparaat daarna meteen.",
        "wiz.ready.enter_password": "Voer hieronder het wachtwoord in om het apparaat in de app te openen.",
        "wiz.ready.from_now": "Vanaf nu staat het op",
        "wiz.ready.hotspot_off": "De hotspot “Off The Cloud” wordt over een minuut uitgeschakeld.",
        "wiz.ready.installed": "Alles is geïnstalleerd.",
        "wiz.ready.lives_at": "Het staat op \u003cb\u003e{domain}\u003c/b\u003e.",
        "wiz.ready.local_hint": "Alles is geïnstalleerd. Op je thuisnetwerk is het apparaat bereikbaar op",
        "wiz.ready.local_open": "Open dat adres vanaf elk apparaat thuis – je wordt eerst gevraagd het wachtwoord van de eigenaar te kiezen. Wil je het later overal kunnen bereiken? Maak een account aan op {bridge} en doorloop de configuratie opnieuw.",
        "wiz.ready.open_address": "Open dat adres in je browser – onthoud het, want zo bereik je je apparaat overal.",
        "wiz.ready.owner_password": "Je wordt eerst gevraagd het wachtwoord van de eigenaar te kiezen.",
        "wiz.ready.title": "Klaar 🎉",
        "wiz.wifi.bands_signal": "{bands} GHz · {signal}%",
        "wiz.wifi.choose_app": "Kies het netwerk dat het apparaat moet gebruiken: 2,4 of 5 GHz.",
        "wiz.wifi.choose_hotspot": "Kies het netwerk dat het apparaat moet gebruiken. Via deze hotspot maakt het verbinding op 2,4 GHz en stapt het na de configuratie over op 5 GHz; voor een netwerk dat alleen 5 GHz is, heb je “Nieuw apparaat instellen” (Bluetooth) in de app nodig.",
        "wiz.wifi.enter_password": "Voer het wachtwoord voor {ssid} in",
        "wiz.wifi.failed": "Mislukt",
        "wiz.wifi.hotspot_back": "De hotspot is terug – maak er opnieuw verbinding mee.",
        "wiz.wifi.join_failed": "Kan geen verbinding maken met dat netwerk – controleer het wachtwoord.",
        "wiz.wifi.join_rejected": "Kan geen verbinding maken met dat netwerk – controleer het wachtwoord en probeer het opnieuw.",
        "wiz.wifi.joining_title": "1 · Verbinden met {ssid}",
        "wiz.wifi.joining_wait": "Verbinden en wachten op internet – dit kan een minuut duren.",
        "wiz.wifi.keep_open": "Houd deze pagina open – hij gaat vanzelf verder zodra het apparaat online is.",
        "wiz.wifi.none": "Nog geen netwerken – tik op Opnieuw zoeken.",
        "wiz.wifi.online": "Het apparaat is online. Je kunt doorgaan of verbinding maken met een ander netwerk.",
        "wiz.wifi.online_via": "Het apparaat is online via \u003cb\u003e{ssid}\u003c/b\u003e. Je kunt doorgaan of verbinding maken met een ander netwerk.",
        "wiz.wifi.only_5ghz": "Alleen 5 GHz – gebruik de app om verbinding te maken",
        "wiz.wifi.password_for": "Wachtwoord voor {ssid}",
        "wiz.wifi.pick_first": "Kies eerst een netwerk",
        "wiz.wifi.reconnect": "\u003cb\u003eVerlies je de verbinding, maak dan opnieuw verbinding met de wifi “Off The Cloud”.\u003c/b\u003e De hotspot start een paar seconden opnieuw om naar het kanaal van je netwerk over te schakelen; de meeste telefoons maken vanzelf weer verbinding, maar als de jouwe dat niet doet, kies dan opnieuw “Off The Cloud” in je wifi-instellingen en kom terug naar deze pagina.",
        "wiz.wifi.scan_again": "Opnieuw zoeken",
        "wiz.wifi.scanning": "Zoeken…",
        "wiz.wifi.title": "1 · Maak verbinding met je wifi",
        "wiz.wifi.wrong_password": "Een onjuist wachtwoord verschijnt hier als fout; probeer het dan gewoon opnieuw."
    },
    "pt": {
        "common.accept": "Aceitar",
        "common.add": "Adicionar",
        "common.apply": "Aplicar",
        "common.back": "Voltar",
        "common.cancel": "Cancelar",
        "common.choose": "Escolher",
        "common.close": "Fechar",
        "common.connect": "Ligar",
        "common.connecting": "A ligar…",
        "common.create": "Criar",
        "common.decline": "Recusar",
        "common.delete": "Eliminar",
        "common.deleting": "A eliminar…",
        "common.deselect": "Desmarcar",
        "common.disconnect": "Desligar",
        "common.done": "Concluído",
        "common.download": "Transferir",
        "common.loading": "A carregar…",
        "common.next": "Seguinte",
        "common.ok": "OK",
        "common.open": "Abrir",
        "common.password": "Palavra-passe",
        "common.pause": "Pausar",
        "common.quit": "Sair",
        "common.refresh": "Atualizar",
        "common.remove": "Remover",
        "common.rename": "Mudar o nome",
        "common.resume": "Retomar",
        "common.save": "Guardar",
        "common.search": "Pesquisar",
        "common.select": "Selecionar",
        "common.send": "Enviar",
        "common.settings": "Definições",
        "common.share": "Partilhar",
        "common.try_again": "Tentar novamente",
        "common.update": "Atualizar",
        "common.updating": "A atualizar…",
        "common.upload": "Carregar",
        "wiz.account.accept_terms": "Aceito os \u003cterms\u003etermos de utilização\u003c/terms\u003e e li o \u003cprivacy\u003eaviso de privacidade\u003c/privacy\u003e.",
        "wiz.account.checking": "A verificar…",
        "wiz.account.choose_country": "Escolher…",
        "wiz.account.code_hint": "Em qualquer dispositivo, abre a página da tua conta na ponte, \u003cb\u003e{bridge}/account\u003c/b\u003e, inicia sessão (com a tua palavra-passe, o Google ou a Apple) e toca em \u003cb\u003eObter um código de configuração\u003c/b\u003e.",
        "wiz.account.code_label": "Código de configuração",
        "wiz.account.code_placeholder": "p. ex. K7PX2M4Q",
        "wiz.account.confirm_hint": "Enviámos uma ligação para \u003cb\u003e{email}\u003c/b\u003e. Abre-a (neste telemóvel ou noutro dispositivo) para confirmares que o endereço é teu e depois volta aqui e continua. Não a encontras? Procura na pasta de spam.",
        "wiz.account.confirm_title": "3 · Confirma o teu email",
        "wiz.account.confirmed": "Já confirmei: continuar",
        "wiz.account.continue_apple": "Continuar com a Apple",
        "wiz.account.continue_email": "Continuar com email",
        "wiz.account.continue_google": "Continuar com o Google",
        "wiz.account.countries_failed": "Não foi possível carregar a lista de países: verifica se o dispositivo está online",
        "wiz.account.country": "País de residência",
        "wiz.account.create": "Criar conta",
        "wiz.account.email": "Email",
        "wiz.account.have_code": "Tenho um código de configuração",
        "wiz.account.heading": "3 · A tua conta Off The Cloud",
        "wiz.account.intro": "Uma conta associa o nome deste dispositivo a ti, para que lhe possas aceder a partir de qualquer lado, partilhar com amigos e substituí-lo se alguma vez se perder.",
        "wiz.account.name": "Nome",
        "wiz.account.not_confirmed": "Ainda não está confirmado: abre a ligação no email (acabámos de a enviar novamente).",
        "wiz.account.one_moment": "Um momento…",
        "wiz.account.or_email": "ou com o teu email",
        "wiz.account.password_min": "Palavra-passe (8 caracteres ou mais)",
        "wiz.account.provider_hint": "Registaste-te com o Google ou a Apple? Noutro dispositivo, abre a página da tua conta na ponte, \u003cb\u003e{bridge}/account\u003c/b\u003e, obtém um código de configuração e escolhe «Tenho um código de configuração».",
        "wiz.account.sign_in": "Iniciar sessão",
        "wiz.account.signed_in_as": "Sessão iniciada como \u003cb\u003e{email}\u003c/b\u003e. O nome do dispositivo vai ser registado nesta conta.",
        "wiz.account.signin_cancelled": "Início de sessão cancelado",
        "wiz.account.signin_failed": "Não foi possível iniciar sessão",
        "wiz.account.skip": "Continuar sem conta",
        "wiz.account.skip_hint": "Sem uma conta, o dispositivo não usa a ponte. Continua a funcionar como NAS na tua rede doméstica: ficheiros, cópia de segurança de fotos a partir das apps e os clientes de sincronização quando estás em casa, a galeria de fotos, as etiquetas e as pessoas. O que precisa da ponte: aceder ao dispositivo fora de casa, a parte social (amigos, publicações, comentários), ligações de partilha que funcionam em qualquer lado e as notificações push. Podes ligar o dispositivo à ponte mais tarde, em Definições na app web.",
        "wiz.account.surname": "Apelido",
        "wiz.account.terms_bridge": "Criar uma conta só é necessário para usar a nossa ponte: vais ter até \u003cb\u003e5 domínios\u003c/b\u003e como \u003ci\u003enome\u003c/i\u003e.{bridge} para te ligares ao teu dispositivo e também aos dispositivos dos teus amigos e familiares. Se só quiseres usar este dispositivo na tua rede local como NAS ou com o Tailscale Funnel, salta a criação da conta.",
        "wiz.account.terms_price": "Uma conta com até 5 domínios é \u003cb\u003egratuita durante os primeiros dois anos\u003c/b\u003e e, depois desse período, custa 9,99 euros + IVA por ano (enviamos-te um email antes de o período terminar). Se precisares de mais domínios, contacta-nos em \u003cb\u003einfo@off-the.cloud\u003c/b\u003e.",
        "wiz.account.title": "3 · A tua conta",
        "wiz.account.use_another": "Usar outra conta",
        "wiz.account.use_code": "Usar este código",
        "wiz.action.continue": "Continuar",
        "wiz.action.continue_failed": "Não foi possível continuar",
        "wiz.bridge.account_check_unavailable": "não foi possível verificar a tua conta neste momento",
        "wiz.bridge.claim_unavailable": "não é possível reservar esse nome neste momento",
        "wiz.bridge.domain_limit": "uma conta pode registar até 5 domínios: para mais (2 euros por ano cada um), escreve para info@off-the.cloud",
        "wiz.bridge.email_taken": "já existe uma conta com esse email: inicia sessão",
        "wiz.bridge.invalid_credentials": "email ou palavra-passe incorretos: se te registaste com o Google ou a Apple, usa essa opção (na página da conta e depois com um código de configuração)",
        "wiz.bridge.invalid_email": "esse endereço de email não parece correto",
        "wiz.bridge.invalid_name": "esse nome não pode ser usado",
        "wiz.bridge.login_required": "inicia sessão para registar um nome",
        "wiz.bridge.name_check_unavailable": "não foi possível verificar esse nome neste momento",
        "wiz.bridge.name_held": "esse nome foi libertado recentemente e fica reservado para o anterior proprietário durante 30 dias",
        "wiz.bridge.name_taken": "esse nome já está ocupado",
        "wiz.bridge.password_too_short": "a palavra-passe precisa de ter pelo menos 8 caracteres",
        "wiz.bridge.profile_incomplete": "o nome, o apelido e o país de residência são obrigatórios",
        "wiz.bridge.setup_code_check_unavailable": "não foi possível verificar esse código de configuração neste momento",
        "wiz.bridge.setup_unavailable": "não foi possível iniciar a configuração",
        "wiz.bridge.signin_unavailable": "não é possível iniciar sessão neste momento",
        "wiz.bridge.signup_unavailable": "não foi possível criar a conta neste momento",
        "wiz.bridge.terms_not_accepted": "aceita primeiro os termos de utilização: inicia sessão na tua conta em off-the.cloud/account e aceita-os lá",
        "wiz.bridge.terms_required": "tens de aceitar os termos",
        "wiz.bridge.too_many_attempts": "demasiadas tentativas falhadas; tenta novamente mais tarde",
        "wiz.bridge.too_many_claims": "espera um momento antes de tentares outro nome",
        "wiz.bridge.too_many_signups": "demasiados registos a partir deste endereço; tenta novamente mais tarde",
        "wiz.disks.continue_without": "Continuar sem discos adicionais",
        "wiz.disks.found": "Foi encontrado um armazenamento Off The Cloud existente",
        "wiz.disks.looking": "A analisar os discos ligados…",
        "wiz.disks.mounted": "atualmente montado",
        "wiz.disks.none": "Não foram encontrados discos adicionais. Liga-os e toca em Procurar novamente, ou continua sem eles.",
        "wiz.disks.pick_hint": "Escolhe \u003cb\u003edois\u003c/b\u003e discos para os pôr em espelho como RAID1: um pode avariar sem se perder nada. Não escolhas nenhum para manteres tudo no cartão SD por agora.",
        "wiz.disks.pick_second": "Escolhe um segundo disco para RAID1, ou nenhum.",
        "wiz.disks.recover": "Recuperar este dispositivo",
        "wiz.disks.recover_hint": "Se este Pi está a substituir um que avariou, recupera-o: tudo o que está nestes discos (fotos, ficheiros, o nome e a identidade na ponte) volta a ficar como estava, e não é preciso um nome novo.",
        "wiz.disks.rescan": "Procurar novamente",
        "wiz.disks.start_failed": "Não foi possível iniciar",
        "wiz.disks.start_fresh": "Começar do zero",
        "wiz.disks.title": "2 · Armazenamento",
        "wiz.disks.wipe_continue": "Apagar os dois e continuar",
        "wiz.disks.wipe_warning": "⚠ Os dois discos vão ser apagados.",
        "wiz.disks.wipe_warning_storage": "⚠ Os dois discos vão ser apagados, incluindo o armazenamento Off The Cloud existente neles.",
        "wiz.disks.with_database": "com a respetiva base de dados",
        "wiz.done.hint": "Este dispositivo já concluiu a configuração.",
        "wiz.done.open": "Abrir {domain}",
        "wiz.done.open_app": "Abrir a app",
        "wiz.done.title": "Já configurado",
        "wiz.error.account_chosen": "foi escolhida uma conta",
        "wiz.error.already_set_up": "este dispositivo já está configurado",
        "wiz.error.choose_network": "escolhe uma rede",
        "wiz.error.claim_failed": "não foi possível reservar esse nome",
        "wiz.error.confirm_wipe": "esses discos contêm uma matriz existente: confirma que queres apagá-la",
        "wiz.error.disks_choice": "escolhe dois discos para RAID1, ou nenhum",
        "wiz.error.invalid_setup_code": "esse código de configuração não é válido ou expirou",
        "wiz.error.login_required": "inicia primeiro sessão na tua conta Off The Cloud",
        "wiz.error.name_check_failed": "não foi possível verificar esse nome neste momento: tenta novamente",
        "wiz.error.name_first": "escolhe primeiro o nome do dispositivo",
        "wiz.error.name_rule": "apenas letras minúsculas, algarismos e hífenes",
        "wiz.error.needs_password": "a rede {ssid} precisa de uma palavra-passe",
        "wiz.error.no_array": "não existe nenhuma matriz para recuperar",
        "wiz.error.not_found": "não encontrado",
        "wiz.error.offline": "sem ligação à internet: não é possível contactar a ponte {bridge}",
        "wiz.error.only_5ghz": "a rede {ssid} é de 5 GHz: o ponto de acesso não a consegue seguir. Configura antes o dispositivo com «Configurar um novo dispositivo» da app (Bluetooth) ou escolhe uma rede de 2,4 GHz.",
        "wiz.error.password_too_short": "a palavra-passe tem de ter 8 caracteres ou mais",
        "wiz.error.password_unreadable": "não foi possível ler a palavra-passe: recarrega a página e tenta novamente",
        "wiz.error.picture_unusable": "não foi possível usar a foto de perfil: escolhe outra",
        "wiz.error.rebind_failed": "não foi possível atualizar as definições do dispositivo: {detail}",
        "wiz.error.setup_code_unavailable": "não foi possível verificar esse código de configuração neste momento: tenta novamente",
        "wiz.error.signin_failed": "não foi possível iniciar sessão",
        "wiz.error.unknown_action": "ação desconhecida",
        "wiz.error.unreachable": "não foi possível contactar a ponte {bridge}: {detail}",
        "wiz.install.almost": "5 · Falta pouco",
        "wiz.install.app_box": "A instalação demora cerca de 20 minutos. \u003cb\u003eMantém este ecrã aberto\u003c/b\u003e e o telemóvel perto do dispositivo para a acompanhares aqui; quando terminar, a app oferece-se para ligar a \u003cb\u003e{domain}\u003c/b\u003e. Fechaste-o? Abre novamente \u003cb\u003eConfigurar um novo dispositivo\u003c/b\u003e e o progresso volta a aparecer.",
        "wiz.install.browser_box": "\u003cb\u003eJá podes fechar esta janela e desligar-te.\u003c/b\u003e A instalação demora cerca de 20 minutos. Quando estiver concluída, abre \u003cb\u003ehttps://{domain}\u003c/b\u003e a partir de qualquer rede. Para veres o progresso entretanto, liga-te novamente à rede Wi-Fi «Off The Cloud» e esta página volta a aparecer.",
        "wiz.install.check_again": "Verificar novamente",
        "wiz.install.checking_with": "A verificar com a ponte {bridge}",
        "wiz.install.error.array_not_assembled": "o disco {disk} ainda contém uma matriz RAID que não foi possível montar: a instalação recusa-se a apagá-la. Para recomeçar mesmo do zero, executa: mdadm --zero-superblock {disk}",
        "wiz.install.error.download": "não foi possível transferir {url}",
        "wiz.install.error.download_installer": "não foi possível transferir o instalador verificado",
        "wiz.install.error.exit_code": "o instalador terminou com o código {code}",
        "wiz.install.error.install_web": "não foi possível instalar a app web",
        "wiz.install.error.log_failed": "não foi possível abrir o registo da instalação: {detail}",
        "wiz.install.error.mariadb": "o mariadb não arrancou em 60 s: verifica 'systemctl status mariadb'",
        "wiz.install.error.owner_password": "não foi possível definir a palavra-passe do proprietário",
        "wiz.install.error.pinned_hash": "{url} não corresponde ao hash fixado",
        "wiz.install.error.recovery_no_array": "foi pedida a recuperação, mas não foi possível montar nenhuma matriz RAID1 existente a partir de {disk1}/{disk2}: não foi apagado nada. Verifica se os dois discos estão ligados e tenta novamente.",
        "wiz.install.error.stage_source": "não foi possível preparar o código-fonte",
        "wiz.install.error.start_failed": "não foi possível iniciar o instalador: {detail}",
        "wiz.install.error.tag_list": "não foi possível descompactar a lista de etiquetas",
        "wiz.install.error.unpack_web": "não foi possível descompactar a app web",
        "wiz.install.failed": "Falhou",
        "wiz.install.hint": "A transferir e a configurar tudo. Num Raspberry Pi isto demora algum tempo: mantém o dispositivo ligado à corrente.",
        "wiz.install.log": "Registo",
        "wiz.install.offline_hint": "A instalação terminou, mas a ponte {bridge} não vê \u003cb\u003e{domain}\u003c/b\u003e ligado. Normalmente basta esperar mais um pouco.",
        "wiz.install.offline_title": "5 · Instalado, mas ainda não acessível",
        "wiz.install.open_anyway": "Abrir mesmo assim",
        "wiz.install.starting": "A iniciar…",
        "wiz.install.step.account": "Conta de serviço otc",
        "wiz.install.step.build": "Compilar o binário otc",
        "wiz.install.step.directories": "Pastas de execução",
        "wiz.install.step.face_models": "Modelos de reconhecimento facial (issue #52, ~10 MB no total, só transferidos uma vez)",
        "wiz.install.step.go": "Go {version} ({arch})",
        "wiz.install.step.installed": "Instalado",
        "wiz.install.step.onnx": "ONNX Runtime {version} ({arch})",
        "wiz.install.step.packages": "apt-get update + pacotes base",
        "wiz.install.step.proto": "Gerar proto/generated (protoc + protoc-gen-go)",
        "wiz.install.step.service": "Base de dados, configuração e o serviço systemd",
        "wiz.install.step.source": "Código-fonte da versão {version} (verificado)",
        "wiz.install.step.storage": "Armazenamento RAID1 ({path})",
        "wiz.install.step.tagging_model": "Modelo de etiquetagem RAM++ (~870 MB, só transferido uma vez)",
        "wiz.install.step.web": "App web (versão {version}, verificada)",
        "wiz.install.title_installing": "5 · A instalar",
        "wiz.install.title_recovering": "5 · A recuperar",
        "wiz.install.waiting": "Instalado. À espera de que o dispositivo se ligue à ponte…",
        "wiz.install.waiting_as": "Instalado. À espera de que o dispositivo se ligue à ponte como \u003cb\u003e{domain}\u003c/b\u003e…",
        "wiz.name.available": "{domain} está disponível",
        "wiz.name.check_failed": "Não foi possível verificar esse nome",
        "wiz.name.hint": "Este nome passa a ser o endereço dele na internet, para ti e para os teus amigos. Letras, algarismos e hífenes.",
        "wiz.name.install": "Instalar",
        "wiz.name.install_failed": "Não foi possível iniciar a instalação",
        "wiz.name.label": "Nome do dispositivo",
        "wiz.name.local_hint": "Escolheste continuar sem conta, por isso o dispositivo não recebe nenhum endereço na internet. Na tua rede doméstica, responde como \u003cb\u003eotc.local\u003c/b\u003e.",
        "wiz.name.local_title": "4 · Sem ponte",
        "wiz.name.placeholder": "p. ex. casa",
        "wiz.name.rebind_hint": "O dispositivo voltou, mas não está a chegar à ponte como \u003cb\u003e{domain}\u003c/b\u003e: esse nome pode ter sido libertado. Escolhe um nome para o voltar a registar; tudo o resto já foi recuperado.",
        "wiz.name.rebind_hint_old": "O dispositivo voltou, mas não está a chegar à ponte com \u003cb\u003eo nome antigo\u003c/b\u003e: esse nome pode ter sido libertado. Escolhe um nome para o voltar a registar; tudo o resto já foi recuperado.",
        "wiz.name.rebind_title": "Dá um nome ao dispositivo recuperado",
        "wiz.name.register": "Registar",
        "wiz.name.reserve_failed": "Não foi possível reservar esse nome",
        "wiz.name.reserving": "A reservar…",
        "wiz.name.someone_else": "{domain} pertence a outra pessoa: escolhe outro nome",
        "wiz.name.taken": "{domain} já está ocupado. Se for um dos teus dispositivos, continuar retira-lhe este endereço e atribui-o a este dispositivo.",
        "wiz.name.title": "4 · Dá um nome ao teu dispositivo",
        "wiz.name.yours_offline": "{domain} pertence a um dos teus dispositivos (sem ligação neste momento). Se continuares, esse dispositivo é retirado deste endereço e este fica com ele.",
        "wiz.name.yours_online": "{domain} está a ser usado por um dos teus dispositivos, com ligação neste momento. Se continuares, esse dispositivo é retirado deste endereço (continua a funcionar em casa) e este fica com ele.",
        "wiz.owner.hint": "8 caracteres ou mais. Encripta tudo o que está no dispositivo e não pode ser recuperada: guarda-a num local seguro. É com ela que inicias sessão no dispositivo.",
        "wiz.owner.mismatch": "As duas palavras-passe não coincidem.",
        "wiz.owner.password_label": "Palavra-passe do dispositivo",
        "wiz.owner.repeat_label": "Repetir a palavra-passe",
        "wiz.owner.too_short": "8 caracteres ou mais.",
        "wiz.page.blink": "Fazer piscar a luz",
        "wiz.page.blinking": "A piscar: procura a luz verde intermitente",
        "wiz.page.device_id": "Este é o dispositivo \u003cb\u003e{id}\u003c/b\u003e.",
        "wiz.page.lead": "Vamos configurar o teu dispositivo.",
        "wiz.page.title": "Configuração do Off The Cloud",
        "wiz.profile.about_label": "Sobre ti",
        "wiz.profile.about_placeholder": "Uma frase sobre ti",
        "wiz.profile.drag_hint": "Arrasta a foto para centrares o teu rosto.",
        "wiz.profile.faces_hint": "Agrupa as mesmas pessoas nas tuas fotos para poderes pesquisar por pessoa. Funciona apenas neste dispositivo, mas analisa todas as pessoas que aparecem nas tuas fotos, não apenas a ti. Fica desativado se não o marcares.",
        "wiz.profile.faces_label": "Reconhecer rostos nas minhas fotos",
        "wiz.profile.name_label": "O teu nome",
        "wiz.profile.name_placeholder": "Como os teus amigos te veem",
        "wiz.profile.optional": "(opcional)",
        "wiz.profile.optional_hint": "Tudo isto é opcional: podes defini-lo mais tarde em Definições.",
        "wiz.profile.picture_label": "Foto de perfil",
        "wiz.profile.title": "O teu perfil",
        "wiz.profile.zoom": "Zoom",
        "wiz.ready.back": "O teu dispositivo voltou, com tudo o que tinha.",
        "wiz.ready.choose_password": "Escolhe a palavra-passe dele abaixo: a app abre-o logo a seguir.",
        "wiz.ready.enter_password": "Introduz a palavra-passe dele abaixo para o abrires na app.",
        "wiz.ready.from_now": "A partir de agora, o endereço dele é",
        "wiz.ready.hotspot_off": "O ponto de acesso «Off The Cloud» desliga-se dentro de um minuto.",
        "wiz.ready.installed": "Está tudo instalado.",
        "wiz.ready.lives_at": "O endereço dele é \u003cb\u003e{domain}\u003c/b\u003e.",
        "wiz.ready.local_hint": "Está tudo instalado. Na tua rede doméstica, o dispositivo responde em",
        "wiz.ready.local_open": "Abre esse endereço a partir de qualquer dispositivo em casa: primeiro, vai pedir-te que escolhas a palavra-passe do proprietário. Queres que esteja acessível a partir de qualquer lado mais tarde? Cria uma conta na ponte, em {bridge}, e volta a fazer a configuração.",
        "wiz.ready.open_address": "Abre esse endereço no teu navegador e memoriza-o: é assim que chegas ao teu dispositivo a partir de qualquer lado.",
        "wiz.ready.owner_password": "Primeiro, vai pedir-te que escolhas a palavra-passe do proprietário.",
        "wiz.ready.title": "Tudo pronto 🎉",
        "wiz.wifi.bands_signal": "{bands} GHz · {signal}%",
        "wiz.wifi.choose_app": "Escolhe a rede que o dispositivo deve usar: 2,4 ou 5 GHz.",
        "wiz.wifi.choose_hotspot": "Escolhe a rede que o dispositivo deve usar. Através deste ponto de acesso, liga-se em 2,4 GHz e passa para 5 GHz depois de configurado; uma rede só de 5 GHz precisa de «Configurar um novo dispositivo» da app (Bluetooth).",
        "wiz.wifi.enter_password": "Introduz a palavra-passe da rede {ssid}",
        "wiz.wifi.failed": "Falhou",
        "wiz.wifi.hotspot_back": "O ponto de acesso voltou: liga-te novamente a ele.",
        "wiz.wifi.join_failed": "Não foi possível ligar a essa rede: verifica a palavra-passe.",
        "wiz.wifi.join_rejected": "Não foi possível ligar a essa rede: verifica a palavra-passe e tenta novamente.",
        "wiz.wifi.joining_title": "1 · A ligar a {ssid}",
        "wiz.wifi.joining_wait": "A ligar e à espera da internet: até um minuto.",
        "wiz.wifi.keep_open": "Mantém esta página aberta: continua sozinha quando o dispositivo estiver online.",
        "wiz.wifi.none": "Ainda não há redes: toca em Procurar novamente.",
        "wiz.wifi.online": "O dispositivo está online. Podes continuar ou ligá-lo a outra rede.",
        "wiz.wifi.online_via": "O dispositivo está online através de \u003cb\u003e{ssid}\u003c/b\u003e. Podes continuar ou ligá-lo a outra rede.",
        "wiz.wifi.only_5ghz": "Só 5 GHz: usa a app para ligar a esta rede",
        "wiz.wifi.password_for": "Palavra-passe da rede {ssid}",
        "wiz.wifi.pick_first": "Escolhe primeiro uma rede",
        "wiz.wifi.reconnect": "\u003cb\u003eSe perderes a ligação, volta a ligar-te à rede Wi-Fi «Off The Cloud».\u003c/b\u003e O ponto de acesso reinicia durante alguns segundos para mudar para o canal da tua rede; a maioria dos telemóveis volta a ligar-se sozinha, mas se o teu não o fizer, escolhe novamente «Off The Cloud» nas definições de Wi-Fi e volta a esta página.",
        "wiz.wifi.scan_again": "Procurar novamente",
        "wiz.wifi.scanning": "A procurar…",
        "wiz.wifi.title": "1 · Ligar ao teu Wi-Fi",
        "wiz.wifi.wrong_password": "Uma palavra-passe errada aparece aqui como erro; basta tentares novamente."
    }
}
# END GENERATED I18N


# ---------------------------------------------------------------------------
# Text (docs/i18n.md, "Setup wizard")
# ---------------------------------------------------------------------------
# The page shows its text in the person's language, from I18N above
# (i18n/strings/en/wiz.json and common.json; make i18n writes it). The
# language comes from ?lang= on the page - the apps pass theirs, and the
# page sends it back on every /api call, since Bluetooth forwards the path
# and not the headers - else from Accept-Language, else it is English. The
# page builds its views node by node: no translated text or argument is
# ever parsed as markup. The backend's answers keep today's English as
# "error" (older apps and pages read it as it is) and add a "code" and its
# "args", which the page words in the person's language (wiz.error.<code>).

PLACEHOLDER_RE = re.compile(r"\{([a-z][a-z0-9_]*)\}")


def english(key, args=None):
    """key's English with args put in - what the page shows an English
    reader, and what the backend answers as "error". None when the key or
    one of the arguments it uses is missing."""
    args = args or {}
    text = I18N["en"].get(key)
    if isinstance(text, dict):  # a plural, by English rules
        text = text.get("one") if args.get("count") == 1 else text.get("other")
    if not isinstance(text, str) or any(a not in args for a in PLACEHOLDER_RE.findall(text)):
        return None
    return PLACEHOLDER_RE.sub(lambda m: str(args[m.group(1)]), text)


def mark_args(raw):
    """The arguments of an install.sh mark: a JSON object of names to
    strings, {} when there are none, None when they can't be read."""
    if not raw:
        return {}
    try:
        args = json.loads(raw)
    except ValueError:
        return None
    if not isinstance(args, dict) or not all(
            isinstance(k, str) and re.fullmatch(r"[a-z][a-z0-9_]*", k) and isinstance(v, str) for k, v in args.items()):
        return None
    return args


def err_body(code, **args):
    """An error answer of the wizard's own: its English (wiz.error.<code>),
    the code, and the arguments the page words it with."""
    return {"error": english("wiz.error." + code, args) or code, "code": code, "args": args}


def bridge_error(data, code):
    """A refusal the bridge answered, passed on: its English and its code as
    they came (the page words the ones it knows, wiz.bridge.<code>), or ours
    (wiz.error.<code>) when it sent no text."""
    data = data if isinstance(data, dict) else {}
    if "error" not in data:
        return err_body(code)
    out = {"error": data["error"]}
    if isinstance(data.get("code"), str) and data["code"]:
        out["code"] = data["code"]
    if isinstance(data.get("args"), dict):
        out["args"] = data["args"]
    return out


def lang_code(tag):
    """The language this wizard has for a language tag ("es", "pt-PT",
    "de_DE.UTF-8"), matched by its base language; "" for none."""
    base = re.split(r"[-_.@]", str(tag).strip(), maxsplit=1)[0].lower()
    return base if re.fullmatch(r"[a-z]{2,3}", base) and base in I18N else ""


def accept_language(header):
    """The language an Accept-Language header prefers among this wizard's;
    "" for none."""
    prefs = []
    for i, part in enumerate(str(header or "")[:512].split(",")[:32]):
        tag, _, params = part.partition(";")
        q = 1.0
        for p in params.split(";"):
            name, _, value = p.partition("=")
            if name.strip().lower() == "q":
                try:
                    q = float(value)
                except ValueError:
                    q = 0.0
        if q > 0:
            prefs.append((-q, i, tag))
    for _, _, tag in sorted(prefs):
        code = lang_code(tag)
        if code:
            return code
    return ""


I18N_SLOT = '<script type="application/json" id="i18n">{}</script>'
_pages = {}


def page_in(code, chosen):
    """PAGE with its dictionaries for language code: English, and code's
    translations, as JSON data the page reads with JSON.parse (escaped so
    nothing in it can end the script element). chosen: code came from the
    request, so the page sends it back with every call."""
    if (code, chosen) not in _pages:
        data = json.dumps({"lang": code, "chosen": chosen, "en": I18N["en"],
                           "texts": I18N.get(code, {}) if code != "en" else {}},
                          ensure_ascii=False, separators=(",", ":"), sort_keys=True)
        data = data.replace("<", "\\u003c").replace(">", "\\u003e").replace("&", "\\u0026")
        _pages[(code, chosen)] = PAGE.replace(I18N_SLOT, I18N_SLOT.replace(">{}<", ">" + data + "<"), 1).encode()
    return _pages[(code, chosen)]


def run(cmd, timeout=30):
    try:
        return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    except Exception as e:  # noqa: BLE001 - reported to the page, never fatal here
        return subprocess.CompletedProcess(cmd, 1, "", str(e))


def read_json(path, default):
    try:
        return json.loads(Path(path).read_text())
    except Exception:  # noqa: BLE001
        return default


def write_json(path, data, mode=0o644):
    atomic_write(path, json.dumps(data), mode)


def atomic_write(path, text, mode=0o644):
    """Root writes into /var/lib/otc, which belongs to the otc user once
    installed: a plain write (or chmod) followed a symlink planted there.
    A fresh temp file (mkstemp: O_EXCL, a random name - several threads
    write at once) with its final mode from the start, renamed into place:
    a rename replaces a symlink instead of following it, and readers (this
    wizard, network_setup.py, setup_ble.py) never see a half-written
    file."""
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=str(p.parent), prefix=f".{p.name}.", suffix=".tmp")
    try:
        os.fchmod(fd, mode)
        with os.fdopen(fd, "w") as f:
            f.write(text)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, p)
    except BaseException:
        Path(tmp).unlink(missing_ok=True)
        raise


def human_size(size):
    return f"{size / 1e9:.0f} GB" if size >= 1e9 else f"{size / 1e6:.0f} MB"


# ---------------------------------------------------------------------------
# Connectivity, WiFi
# ---------------------------------------------------------------------------

def bridge_reachable():
    """The one connectivity test that matters: can this device reach the
    bridge it is about to register with."""
    try:
        with urllib.request.urlopen(f"https://{CONFIG['bridge']}/check_healty", timeout=4) as r:
            return r.status == 200
    except Exception:  # noqa: BLE001
        return False


def current_ssid():
    res = run(["nmcli", "-t", "-f", "ACTIVE,SSID,DEVICE", "dev", "wifi", "list", "--rescan", "no"])
    for line in res.stdout.splitlines():
        parts = line.split(":")
        if len(parts) >= 3 and parts[0] == "yes" and parts[2] == CONFIG["wifi_interface"]:
            return parts[1]
    return ""


def scan_wifi(rescan=False):
    """The pre-scan network_setup.py left (see CONFIG["scan_file"]), or a
    live scan when asked for one / there is none. Both bands, each network
    with the bands it was seen on: over the hotspot the join happens on
    2.4GHz, over Bluetooth on either (see network_setup.py)."""
    if not rescan:
        cached = read_json(CONFIG["scan_file"], None)
        if cached and cached.get("networks") is not None:
            return cached["networks"]
    res = run(["nmcli", "-t", "-f", "SSID,SIGNAL,SECURITY,FREQ", "dev", "wifi", "list",
               "ifname", CONFIG["wifi_interface"], "--rescan", "yes"], timeout=60)
    best = {}
    for line in res.stdout.splitlines():
        # nmcli -t escapes ':' inside fields as '\:'.
        parts = re.split(r"(?<!\\):", line)
        if len(parts) < 4:
            continue
        ssid = parts[0].replace("\\:", ":")
        if not ssid or ssid == "Off The Cloud":
            continue
        try:
            signal, freq = int(parts[1]), int(parts[3].split()[0])
        except (ValueError, IndexError):
            continue
        band = "2.4" if 2400 <= freq < 2500 else "5" if 4900 <= freq < 5900 else None
        if not band:
            continue
        security = parts[2].strip()
        bands = set(best[ssid]["bands"]) if ssid in best else set()
        bands.add(band)
        if ssid not in best or best[ssid]["signal"] < signal:
            best[ssid] = {"ssid": ssid, "signal": signal, "secured": security != "", "security": security, "freq": freq}
        best[ssid]["bands"] = sorted(bands)
    networks = sorted(best.values(), key=lambda n: -n["signal"])
    write_json(CONFIG["scan_file"], {"at": time.time(), "networks": networks})
    return networks


# ---------------------------------------------------------------------------
# Disks + an existing array to recover
# ---------------------------------------------------------------------------

def list_disks():
    """USB/SATA disks that are not the one the system booted from."""
    res = run(["lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,MODEL,TYPE,TRAN,MOUNTPOINTS"])
    try:
        tree = json.loads(res.stdout)
    except Exception:  # noqa: BLE001
        return []

    def holds_system(node):
        for mp in node.get("mountpoints") or []:
            if mp in ("/", "/boot", "/boot/firmware"):
                return True
        return any(holds_system(c) for c in node.get("children") or [])

    disks = []
    for node in tree.get("blockdevices", []):
        if node.get("type") != "disk" or holds_system(node):
            continue
        name = node.get("name", "")
        if name.startswith(("zram", "loop", "ram", "md")):
            continue
        size = int(node.get("size") or 0)
        disks.append({
            "path": node.get("path"),
            "size": size,
            "size_h": human_size(size),
            "model": (node.get("model") or "").strip(),
            "transport": node.get("tran") or "",
            "in_use": bool(node.get("mountpoints") and any(node.get("mountpoints"))),
        })
    return disks


def parent_disk(dev):
    """/dev/sda1 -> /dev/sda; a whole-disk member is returned as is."""
    res = run(["lsblk", "-no", "PKNAME", dev])
    parent = res.stdout.strip().splitlines()[0].strip() if res.stdout.strip() else ""
    return f"/dev/{parent}" if parent else dev


def detect_recovery():
    """An existing RAID1 array on the attached disks holding an Off The
    Cloud database: what a re-imaged device finds when the old Pi died but
    its disks didn't. Assembling is read-only as far as the data goes;
    install.sh assembles the very same way before it would ever wipe."""
    if CONFIG["dry_run"] and os.environ.get("OTC_SETUP_DRY_RUN_RECOVERY") == "1":
        return {"found": True, "device": "/dev/md0", "members": ["/dev/sda", "/dev/sdb"],
                "size_h": "1000 GB", "has_database": True}
    run(["mdadm", "--assemble", "--scan"], timeout=60)
    try:
        mdstat = Path("/proc/mdstat").read_text()
    except Exception:  # noqa: BLE001
        return {"found": False}
    for line in mdstat.splitlines():
        m = MDSTAT_RE.match(line.strip())
        if not m:
            continue
        device = f"/dev/{m.group(1)}"
        members = sorted({parent_disk("/dev/" + mm.group(1))
                          for tok in m.group(2).split() for mm in [MEMBER_RE.match(tok)] if mm})
        size = 0
        res = run(["lsblk", "-b", "-dno", "SIZE", device])
        try:
            size = int(res.stdout.strip())
        except ValueError:
            pass
        has_db = False
        mnt = Path(CONFIG["recovery_mount"])
        mnt.mkdir(parents=True, exist_ok=True)
        if run(["mount", "-o", "ro", device, str(mnt)]).returncode == 0:
            has_db = (mnt / "mysql" / "otc").is_dir()
            run(["umount", str(mnt)])
        return {"found": True, "device": device, "members": members,
                "size_h": human_size(size), "has_database": has_db}
    return {"found": False}


def wipe_array(arr):
    """The person chose a fresh start over recovering: take the old array
    apart so install.sh's own assemble-before-wipe finds nothing."""
    run(["mdadm", "--stop", arr["device"]], timeout=60)
    for member in arr.get("members", []):
        run(["mdadm", "--zero-superblock", member], timeout=60)
        run(["wipefs", "-a", member], timeout=60)


# ---------------------------------------------------------------------------
# Bridge name + identity
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Sealing the owner password (the page encrypts it before it leaves the
# phone): the hotspot is an open WiFi network and the Bluetooth link is not
# paired, so a password sent as is could be read by anyone nearby. The page
# has no WebCrypto (not a secure context over plain HTTP), so both sides
# are hand-written: RSA-2048 with OAEP (SHA-256, MGF1-SHA-256, empty
# label) - a one-off key pair made when the wizard starts, stdlib only.
# ---------------------------------------------------------------------------

_SMALL_PRIMES = [p for p in range(3, 2000) if all(p % q for q in range(2, int(p ** 0.5) + 1))]


def _probable_prime(n, rounds=40):
    if n < 2:
        return False
    for p in _SMALL_PRIMES:
        if n % p == 0:
            return n == p
    d, r = n - 1, 0
    while d % 2 == 0:
        d //= 2
        r += 1
    for _ in range(rounds):
        a = secrets.randbelow(n - 3) + 2
        x = pow(a, d, n)
        if x in (1, n - 1):
            continue
        for _ in range(r - 1):
            x = pow(x, 2, n)
            if x == n - 1:
                break
        else:
            return False
    return True


def _prime(bits):
    while True:
        c = secrets.randbits(bits) | (1 << (bits - 1)) | (1 << (bits - 2)) | 1
        if _probable_prime(c):
            return c


def _mgf1(seed, length):
    out, counter = b"", 0
    while len(out) < length:
        out += hashlib.sha256(seed + counter.to_bytes(4, "big")).digest()
        counter += 1
    return out[:length]


class SealKey:
    """RSA-2048 for OAEP-SHA-256; generated in the background at start."""

    def __init__(self):
        self.ready = threading.Event()
        threading.Thread(target=self._generate, daemon=True).start()

    def _generate(self):
        e = 65537
        while True:
            p, q = _prime(1024), _prime(1024)
            phi = (p - 1) * (q - 1)
            if p != q and phi % e and (p * q).bit_length() == 2048:
                break
        self.n, self.e, self.d = p * q, e, pow(e, -1, phi)
        self.ready.set()

    def public(self):
        self.ready.wait(60)
        return {"n": format(self.n, "x"), "e": self.e}

    def open(self, sealed_b64):
        """The plaintext bytes, or None when it isn't a valid seal for this key."""
        self.ready.wait(60)
        try:
            c = int.from_bytes(base64.b64decode(sealed_b64), "big")
        except Exception:  # noqa: BLE001
            return None
        k = (self.n.bit_length() + 7) // 8
        if c >= self.n:
            return None
        em = pow(c, self.d, self.n).to_bytes(k, "big")
        h = 32
        if em[0] != 0:
            return None
        masked_seed, masked_db = em[1:1 + h], em[1 + h:]
        seed = bytes(a ^ b for a, b in zip(masked_seed, _mgf1(masked_db, h)))
        db = bytes(a ^ b for a, b in zip(masked_db, _mgf1(seed, k - h - 1)))
        if db[:h] != hashlib.sha256(b"").digest():
            return None
        rest = db[h:].lstrip(b"\x00")
        if not rest or rest[0] != 1:
            return None
        return rest[1:]


SEAL = None  # the SealKey, made in main()


def setup_token():
    """One random token per setup, kept in the state file so a wizard
    restart doesn't orphan the page that already holds it."""
    with _state_lock:
        st = load_state()
        if not st.get("token"):
            st["token"] = secrets.token_urlsafe(32)
            save_state(st)
        return st["token"]


def device_id():
    """A short ID for telling devices apart while several are being set up
    at once: the last 4 hex digits of the Pi's serial number (the machine
    id elsewhere). The Bluetooth name, the setup page and the app's list
    of devices all show it."""
    for path in ("/proc/device-tree/serial-number", "/sys/firmware/devicetree/base/serial-number"):
        try:
            s = Path(path).read_text().strip("\x00\n ")
            if len(s) >= 4:
                return s[-4:].upper()
        except OSError:
            pass
    try:
        for line in Path("/proc/cpuinfo").read_text().splitlines():
            if line.startswith("Serial") and ":" in line:
                s = line.split(":", 1)[1].strip()
                if len(s) >= 4:
                    return s[-4:].upper()
    except OSError:
        pass
    try:
        return Path("/etc/machine-id").read_text().strip()[-4:].upper()
    except OSError:
        return hashlib.sha256(socket.gethostname().encode()).hexdigest()[-4:].upper()

_blinking = threading.Lock()


def blink_led(seconds=15):
    """"Blink its light" on the setup page: flashes the Pi's green activity
    LED so the owner can tell which box is which when setting up several
    at once, then puts its usual trigger back."""
    if not _blinking.acquire(blocking=False):
        return
    try:
        led = next((Path("/sys/class/leds", n) for n in ("ACT", "led0") if Path("/sys/class/leds", n).exists()), None)
        if led is None:
            return
        trigger = (led / "trigger").read_text()
        previous = trigger.split("[", 1)[1].split("]", 1)[0] if "[" in trigger else "none"
        (led / "trigger").write_text("none")
        on = False
        end = time.time() + seconds
        while time.time() < end:
            on = not on
            (led / "brightness").write_text("1" if on else "0")
            time.sleep(0.15)
        (led / "trigger").write_text(previous)
    except OSError as e:
        print(f"[otc-setup] blink failed: {e}")
    finally:
        _blinking.release()


def lan_address():
    """This device's address on the network it uses to reach the bridge."""
    res = run(["ip", "-4", "route", "get", "1.1.1.1"])
    m = re.search(r"\bsrc (\S+)", res.stdout)
    return m.group(1) if m else ""


def beacon_loop():
    """Issue #38 hand-off: while setting up, keep telling the bridge where
    this device is on the LAN, under the setup token, so the wizard page
    left open on the owner's phone (which lost the hotspot when the
    device joined their WiFi) can find it again by polling the bridge.
    Reported every few seconds while online; the bridge forgets it after
    a quarter of an hour."""
    while not Path(CONFIG["setup_done_marker"]).exists():
        addr = lan_address()
        if addr and not addr.startswith("10.42.0.") and bridge_reachable():
            try:
                bridge_post("/api/setup-beacon", {"token": setup_token(), "addr": addr})
            except Exception as e:  # noqa: BLE001
                print(f"[otc-setup] beacon failed: {e}")
        time.sleep(5)


def bridge_get(path, token=None):
    headers = {"Accept": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(f"https://{CONFIG['bridge']}{path}", headers=headers)
    with urllib.request.urlopen(req, timeout=8) as r:
        return r.status, json.loads(r.read().decode() or "{}")


def bridge_answer(path, token=None):
    """bridge_get, but the bridge's error answers come back as (status,
    data), as bridge_post's do, for the page to show (issue #189: a 500
    "could not check that name right now" read as "could not reach").
    Only a bridge that can't be reached raises."""
    try:
        return bridge_get(path, token)
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "{}")
        except Exception:  # noqa: BLE001
            return e.code, {}


def bridge_post(path, body):
    data = json.dumps(body).encode()
    req = urllib.request.Request(f"https://{CONFIG['bridge']}{path}", data=data, method="POST",
                                 headers={"Content-Type": "application/json", "Accept": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "{}")
        except Exception:  # noqa: BLE001
            return e.code, {}


def new_identity():
    return {
        "DEVICE_UUID": str(uuid.uuid4()),
        "BRIDGE_SECRET": secrets.token_hex(24),
        "OTC_DB_PASS": secrets.token_urlsafe(24).replace("-", "").replace("_", ""),
    }


def write_env_file(identity):
    # 0600 from the moment it exists (it was briefly world-readable).
    atomic_write(CONFIG["env_file"],
        "# Generated by the OTC setup wizard. Keep out of git - this is the\n"
        "# device's DB password and bridge secret.\n"
        + "".join(f"{k}={v}\n" for k, v in identity.items()), 0o600)


class Refused(RuntimeError):
    """A refusal for the page: str() is its English, answer its error
    answer (with the code the page words it by)."""

    def __init__(self, answer):
        super().__init__(str(answer.get("error")))
        self.answer = answer


def claim_name(name, setup_token):
    """Reserve name on the bridge with a fresh identity, for the account
    behind setup_token (issue #124). Returns (domain, identity) or raises
    Refused, the bridge's refusal for the page. A name the account already
    owns is handed to this device - that is how a lost device is replaced."""
    identity = new_identity()
    status, data = bridge_post("/api/claim", {
        "name": name, "owner_uuid": identity["DEVICE_UUID"], "secret": identity["BRIDGE_SECRET"],
        "setup_token": setup_token})
    if status != 201:
        raise Refused(bridge_error(data, "claim_failed"))
    return data.get("domain", f"{name}.{CONFIG['bridge']}"), identity


def account_sign_in(action, body):
    """Issue #124: sign in or sign up on the bridge for the person setting
    up, from here rather than the phone's browser - the hotspot's captive
    DNS only lets the device itself reach the bridge. Returns (status, data):
    data carries setup_token on success, error otherwise."""
    return bridge_post(f"/api/account/{action}?for=setup", body)


def setup_token_owner(token):
    """Who a typed setup code belongs to: (status, data). 200 with the
    account, 404 for a code the bridge doesn't know (or has expired), and
    502 with a "try again" message when the bridge couldn't answer
    (issue #189: a failed check read as an invalid code)."""
    try:
        status, data = bridge_answer("/api/account/setup-token-info?token=" + urllib.parse.quote(token))
    except Exception as e:  # noqa: BLE001
        return 502, err_body("unreachable", bridge=CONFIG["bridge"], detail=str(e))
    if status == 200:
        return 200, data
    if status >= 500:
        return 502, bridge_error(data, "setup_code_unavailable") if data.get("error") else err_body("setup_code_unavailable")
    return 404, err_body("invalid_setup_code")


def db_query(sql):
    """One value out of the device's own database, as root over the
    socket (install.sh leaves MariaDB's root on socket auth)."""
    res = run(["mysql", "-N", "-B", "otc", "-e", sql], timeout=30)
    return res.stdout.strip() if res.returncode == 0 else ""


def rebind_recovered_device(domain, identity):
    """A recovered device whose old name is gone from the bridge: give the
    database the new identity so the running service presents it."""
    sql = ("update settings set device_uuid='{u}', subdomain='{d}', bridge_secret='{s}'"
           .format(u=identity["DEVICE_UUID"], d=domain, s=identity["BRIDGE_SECRET"]))
    res = run(["mysql", "otc", "-e", sql], timeout=30)
    if res.returncode != 0:
        raise Refused(err_body("rebind_failed", detail=res.stderr.strip()))
    run(["systemctl", "restart", "otc.service"], timeout=60)


# ---------------------------------------------------------------------------
# The install run, then the bridge check
# ---------------------------------------------------------------------------

class Install:
    def __init__(self):
        self.lock = threading.Lock()
        self.reset()

    def reset(self):
        self.phase = "idle"   # idle | installing | verifying | online | offline | needs_name | failed
        self.recovery = False
        self.domain = ""
        self.step = 0
        self.total = 10
        self.text = ""
        self.detail = ""
        self.error = ""
        # What the page words text and error by, in the person's language:
        # wiz.install.step.<text_code> / wiz.install.error.<error_code> with
        # their args ("" when only the English is known).
        self.text_code, self.text_args = "", {}
        self.error_code, self.error_args = "", {}
        self.mark = None  # install.sh's last @step/@error line: (kind, code, args)
        self.log = []

    def snapshot(self):
        with self.lock:
            return {
                "phase": self.phase, "recovery": self.recovery, "domain": self.domain,
                "step": self.step, "total": self.total, "text": self.text,
                "detail": self.detail, "error": self.error, "log_tail": self.log[-40:],
                "text_code": self.text_code, "text_args": self.text_args,
                "error_code": self.error_code, "error_args": self.error_args,
            }

    def _failed(self, code, **args):
        """The install failed for a reason of the wizard's own (under self.lock)."""
        self.phase = "failed"
        self.error = english("wiz.install.error." + code, args) or code
        self.error_code, self.error_args = code, args

    def start(self, name, disks, recovery):
        with self.lock:
            if self.phase in ("installing", "verifying"):
                return
            self.reset()
            self.phase = "installing"
            self.recovery = recovery
        threading.Thread(target=self._run, args=(name, disks), daemon=True).start()

    def _feed(self, line):
        line = line.rstrip("\n")
        with self.lock:
            m = MARK_RE.search(line)
            if m:
                # Not shown: it stands for the English line that follows,
                # which the page then words in the person's language.
                self.mark = (m.group(1), m.group(2), mark_args(m.group(3)))
                return
            self.log.append(line)
            if len(self.log) > 2000:
                del self.log[:500]
            m = STEP_RE.search(line)
            if m:
                self.step, self.total, self.text = int(m.group(1)), int(m.group(2)), m.group(3)
                self.text_code, self.text_args = self._marked("step", self.text)
                self.detail = ""
                return
            m = ERROR_RE.search(line)
            if m:
                self.error = m.group(1)
                self.error_code, self.error_args = self._marked("error", self.error)
                return
            m = INFO_RE.search(line)
            if m and not m.group(1).startswith("@"):
                self.detail = m.group(1)

    def _marked(self, kind, text):
        """The code and args of install.sh's mark for the English line text,
        when the mark's key says exactly that in English; ("", {}) when not
        (no mark, or install.sh and this wizard disagree: the English shows)."""
        mark, self.mark = self.mark, None
        if mark and mark[0] == kind and mark[2] is not None \
                and english(f"wiz.install.{kind}.{mark[1]}", mark[2]) == text:
            return mark[1], mark[2]
        return "", {}

    def _run(self, name, disks):
        env = dict(os.environ)
        if not self.recovery and Path(CONFIG["owner_password_file"]).exists():
            env["OTC_OWNER_PASSWORD_FILE"] = CONFIG["owner_password_file"]
        if not self.recovery and Path(CONFIG["profile_file"]).exists():
            env["OTC_SETUP_PROFILE_FILE"] = CONFIG["profile_file"]
        if self.recovery:
            # Recovering must never wipe: install.sh refuses to build a
            # fresh array in this mode and stops instead. (It used to get
            # the wipe confirmation here too, and when it missed the array
            # the wizard had assembled as /dev/md127 it went for the disks.)
            env["OTC_RECOVERY"] = "1"
        else:
            # A fresh install on disks the person picked; an old array on
            # them was already taken apart by wipe_array() after they
            # confirmed it.
            env["OTC_RAID_CONFIRM_WIPE"] = "yes"
        # Issue #124: no account, no bridge - the device stays on the home
        # network (install.sh's OTC_BRIDGE_ADDR="").
        if load_state().get("skip_bridge"):
            env["OTC_BRIDGE_ADDR"] = ""
        if len(disks) == 2:
            env["OTC_DISK1"], env["OTC_DISK2"] = disks
        elif len(disks) == 1:
            # A degraded array being recovered: install.sh assembles it by
            # scan; the disk variables only matter when nothing assembles.
            env["OTC_DISK1"] = env["OTC_DISK2"] = disks[0]
        else:
            env["OTC_SKIP_RAID"] = "1"
        if CONFIG["dry_run"]:
            cmd = ["bash", "-c",
                   'for i in 1 2 3 4 5 6 7 8 9 10; do echo "[otc-install] [$i/10] dry-run step $i"; '
                   'echo "[otc-install] doing things for step $i"; sleep 1; done; echo "[otc-install] Done."']
        else:
            # Issue #160: the image's own copy of verified-install.sh (with
            # the release key in /etc/otc) installs the newest *signed*
            # release - nothing from the branch runs. A wizard on an image
            # from before it fetches that same script.
            local_installer = "/usr/local/bin/otc-verified-install"
            if os.path.exists(local_installer):
                cmd = ["bash", local_installer, name]
            else:
                cmd = ["bash", "-c", 'curl -fsSL --retry 3 "$0" | bash -s -- "$1"', CONFIG["install_url"], name]
        Path(CONFIG["install_log"]).parent.mkdir(parents=True, exist_ok=True)
        # /var/log/otc is the otc user's once an install has run: appended
        # to as root without following a symlink put in the log's place.
        try:
            logfd = os.open(CONFIG["install_log"], os.O_WRONLY | os.O_CREAT | os.O_APPEND | os.O_NOFOLLOW, 0o644)
        except OSError as e:
            with self.lock:
                self._failed("log_failed", detail=str(e))
            return
        with os.fdopen(logfd, "a") as logf:
            logf.write(f"\n=== setup wizard: install started {time.strftime('%F %T')} name={name} disks={disks}\n")
            try:
                proc = subprocess.Popen(cmd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                        text=True, bufsize=1)
            except Exception as e:  # noqa: BLE001
                with self.lock:
                    self._failed("start_failed", detail=str(e))
                return
            for line in proc.stdout:
                logf.write(line)
                logf.flush()
                self._feed(line)
            code = proc.wait()
            logf.write(f"=== installer exited with {code}\n")
        with self.lock:
            if code != 0:
                if self.error:
                    self.phase = "failed"
                else:
                    self._failed("exit_code", code=code)
                return
            self.step, self.text = self.total, english("wiz.install.step.installed")
            self.text_code, self.text_args = "installed", {}
        self.verify()

    def verify(self):
        """Wait for the device to show up on the bridge under its domain."""
        if load_state().get("skip_bridge"):
            # Nothing to wait for: a local-only device never dials in.
            with self.lock:
                self.phase = "online"
            self.finish()
            return
        with self.lock:
            self.phase = "verifying"
            self.detail = ""
        # A fresh device, or a recovered one that has just been registered
        # under a new name, has the domain in the wizard's own state; a
        # recovered device otherwise answers to whatever its database says.
        domain = load_state().get("domain", "")
        if self.recovery and not domain:
            if CONFIG["dry_run"]:
                domain = "recovered-dry-run." + CONFIG["bridge"]
            else:
                domain = db_query("select subdomain from settings limit 1") or ""
        with self.lock:
            self.domain = domain
        online = False
        deadline = time.time() + CONFIG["verify_timeout_s"]
        while time.time() < deadline:
            if CONFIG["dry_run"]:
                time.sleep(3)
                online = not CONFIG["dry_run_offline"]
                break
            name = domain.split(".")[0] if domain else ""
            if name:
                try:
                    _, data = bridge_get("/api/device-online?name=" + urllib.parse.quote(name))
                    online = bool(data.get("online"))
                except Exception:  # noqa: BLE001
                    online = False
            if online:
                break
            time.sleep(5)
        with self.lock:
            if online:
                self.phase = "online"
            elif self.recovery:
                # The array was recovered but its name no longer answers on
                # the bridge (released, or never registered): ask for one.
                self.phase = "needs_name"
            else:
                self.phase = "offline"
        if online:
            self.finish()

    def finish(self):
        """The person is being sent to the app. After a grace period for
        them to read the address: mark the setup done (network_setup.py
        then drops the hotspot, which also closes a phone's captive-portal
        sheet), and get out of the way - the real service wants port 80."""
        # A phone on Bluetooth may miss the minute this process has left
        # (a dropped link, a locked screen): setup_ble.py answers it from
        # this instead, as "online" - "Open it anyway" counts as done too.
        try:
            final = state_snapshot()
            final["install"]["phase"] = "online"
            final["install"]["log_tail"] = []
            final["token"] = ""
            write_json(CONFIG["final_state_file"], final)
        except Exception as e:  # noqa: BLE001
            print("[otc-setup] could not save the final state:", e)
        threading.Thread(target=self._exit_later, daemon=True).start()

    def _exit_later(self):
        time.sleep(45)
        # Not touch(): that creates (or updates) a planted symlink's target.
        atomic_write(CONFIG["setup_done_marker"], "")
        time.sleep(15)
        if CONFIG["dry_run"]:
            return
        print("[otc-setup] install complete - handing over to otc.service")
        run(["systemctl", "disable", "otc-setup.service"])
        # Restart otc after this process has exited (it holds port 80).
        run(["systemd-run", "--on-active=3", "--quiet", "systemctl", "restart", "otc.service"])
        os._exit(0)


install = Install()


# ---------------------------------------------------------------------------
# State
# ---------------------------------------------------------------------------

def load_state():
    return read_json(CONFIG["state_file"], {})


def save_state(state):
    write_json(CONFIG["state_file"], state)


# The beacon thread and every request thread read, change and save the
# state file. Held only for that (never across a call to the bridge, which
# would stall every /api/state poll), so each saves just the keys it
# changed on a fresh copy, and none writes back a copy read seconds before.
_state_lock = threading.Lock()


def update_state(changes):
    with _state_lock:
        st = load_state()
        st.update(changes)
        save_state(st)
        return st


def state_snapshot():
    st = load_state()
    join = read_json(CONFIG["join_result"], {})
    return {
        "online": bridge_reachable(),
        "account": {"email": st.get("account_email", ""), "skip_bridge": bool(st.get("skip_bridge"))},
        "ssid": current_ssid(),
        "lan_addr": lan_address(),
        "token": setup_token(),
        "join_result": join,
        "name": st.get("name", ""),
        "domain": st.get("domain", ""),
        "device_id": device_id(),
        "bridge": CONFIG["bridge"],
        "install": install.snapshot(),
        "already_installed": Path(CONFIG["install_complete_marker"]).exists() and not CONFIG["dry_run"],
    }


# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------

class Handler(BaseHTTPRequestHandler):
    server_version = "otc-setup/1"

    def log_message(self, fmt, *args):  # quieter journal
        if "/api/state" not in (args[0] if args else ""):
            print("[otc-setup]", self.address_string(), fmt % args)

    def send_json(self, status, data):
        body = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def via_bluetooth(self):
        """Issue #137: setup_ble.py forwards the app's requests from the
        device itself; everything over the hotspot comes from 10.42.0.x.
        Over Bluetooth the hotspot isn't needed, so 5GHz networks can be
        joined."""
        return self.client_address[0] in ("127.0.0.1", "::1")

    def lang(self):
        """(code, chosen): the page's language - ?lang= (the apps' own, and
        what the page sends back on every call), else Accept-Language, else
        English (chosen False: nothing to send back)."""
        q = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query).get("lang", [""])[0]
        code = lang_code(q) or accept_language(self.headers.get("Accept-Language"))
        return (code, True) if code else ("en", False)

    def read_body(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n <= 0 or n > 256 * 1024:  # the setup profile picture (#178)
            return {}
        try:
            return json.loads(self.rfile.read(n).decode())
        except Exception:  # noqa: BLE001
            return {}

    # --- GET ---------------------------------------------------------------
    def do_GET(self):
        path = urllib.parse.urlparse(self.path).path
        if path == "/":
            body = page_in(*self.lang())
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(body)
        elif path == "/api/state":
            self.send_json(200, state_snapshot())
        elif path == "/api/wifi":
            rescan = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query).get("rescan", ["0"])[0] == "1"
            self.send_json(200, {"networks": scan_wifi(rescan)})
        elif path == "/api/disks":
            self.send_json(200, {"disks": list_disks(), "recovery": detect_recovery()})
        elif path == "/api/pubkey":
            # The key the page seals the owner password with.
            self.send_json(200, SEAL.public())
        elif path == "/api/providers":
            # The sign-in providers the bridge offers ("google", "apple"):
            # inside the app (Bluetooth setup) they get a button - the app
            # runs the sign-in in the phone's own browser sheet, which has
            # internet, and hands back a setup code (issue #137).
            try:
                status, data = bridge_get("/api/account/providers")
                providers = data.get("providers", []) if status == 200 and isinstance(data, dict) else []
            except Exception:  # noqa: BLE001
                providers = []
            self.send_json(200, {"providers": [p for p in providers if p in ("google", "apple")]})
        elif path == "/api/countries":
            # Issue #135: the country picker of the account step. Fetched
            # through the device, not by the phone's browser - the page is
            # served from the hotspot's address, so a direct call to the
            # bridge is cross-origin (and, on the captive portal, may not
            # even reach it), which left the list empty.
            global COUNTRIES
            if not COUNTRIES:
                try:
                    status, data = bridge_get("/api/account/countries")
                    if status == 200 and isinstance(data, dict):
                        COUNTRIES = data
                except Exception as e:  # noqa: BLE001
                    self.send_json(502, err_body("unreachable", bridge=CONFIG["bridge"], detail=str(e)))
                    return
            self.send_json(200, COUNTRIES)
        elif path == "/api/name":
            name = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query).get("name", [""])[0].strip().lower()
            if not NAME_RE.match(name):
                self.send_json(400, err_body("name_rule"))
                return
            try:
                # With the account's setup code, a taken name also says
                # whether it is this account's own and if that device is
                # online (the page warns before moving it).
                status, data = bridge_answer("/api/name-available?name=" + urllib.parse.quote(name),
                                             load_state().get("setup_token"))
                if status != 200 and not data.get("error"):
                    data = err_body("name_check_failed")
                self.send_json(status, data)
            except Exception as e:  # noqa: BLE001
                self.send_json(502, err_body("unreachable", bridge=CONFIG["bridge"], detail=str(e)))
        else:
            # Captive-portal probes (Apple's hotspot-detect.html, Android's
            # generate_204, Windows' connecttest.txt, ...) and anything else:
            # a redirect is what makes every phone open the "sign in" sheet.
            # Always to the hotspot's own address - the probe's Host header
            # is captive.apple.com or the like, which only resolves here
            # while the DNS trick is in effect.
            host = (self.headers.get("Host") or "").split(":")[0]
            target = host if host in (CONFIG["ap_address"], "otc.local", "otc") else CONFIG["ap_address"]
            self.send_response(302)
            self.send_header("Location", f"http://{target}/")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", "0")
            self.end_headers()

    def do_HEAD(self):
        # Same routing as GET, no body - some probes (and curl -I) use it.
        self.do_GET()

    # --- POST --------------------------------------------------------------
    def do_POST(self):
        path = urllib.parse.urlparse(self.path).path
        body = self.read_body()
        if Path(CONFIG["install_complete_marker"]).exists() and not CONFIG["dry_run"]:
            self.send_json(409, err_body("already_set_up"))
            return

        if path == "/api/identify":
            threading.Thread(target=blink_led, daemon=True).start()
            self.send_json(200, {"ok": True})
            return

        if path == "/api/wifi":
            ssid = str(body.get("ssid", ""))[:64]
            password = str(body.get("password", ""))[:128]
            if not ssid:
                self.send_json(400, err_body("choose_network"))
                return
            known = next((n for n in scan_wifi() if n.get("ssid") == ssid), None)
            security = (known or {}).get("security", "")
            if known and known.get("secured") and not password:
                self.send_json(400, err_body("needs_password", ssid=ssid))
                return
            any_band = self.via_bluetooth()
            if known and known.get("bands") == ["5"] and not any_band:
                self.send_json(400, err_body("only_5ghz", ssid=ssid))
                return
            print(f"[otc-setup] join requested: {ssid!r} security={security!r} password={len(password)} chars")
            Path(CONFIG["join_result"]).unlink(missing_ok=True)
            # network_setup.py (root, owns the radio) does the actual join
            # and moves the hotspot onto the joined network's channel; the
            # request is handed over after a moment so the page can say so.
            def later():
                time.sleep(CONFIG["join_delay_s"])
                write_json(CONFIG["join_request"], {"ssid": ssid, "password": password, "security": security, "any_band": any_band}, mode=0o600)
            threading.Thread(target=later, daemon=True).start()
            self.send_json(202, {"ok": True, "delay_s": CONFIG["join_delay_s"]})

        elif path == "/api/account":
            # Issue #124: sign in / sign up / a typed setup code / skip.
            action = str(body.get("action", ""))
            if action == "skip":
                update_state({"skip_bridge": True, "setup_token": "", "account_email": ""})
                self.send_json(200, {"ok": True})
                return
            if action == "code":
                token = str(body.get("setup_token", "")).strip()
                status, who = setup_token_owner(token)
                if status != 200:
                    self.send_json(status, who)
                    return
                update_state({"skip_bridge": False, "setup_token": token, "account_email": who.get("email", "")})
                self.send_json(200, {"email": who.get("email", "")})
                return
            if action not in ("login", "signup"):
                self.send_json(400, err_body("unknown_action"))
                return
            fields = {k: str(body.get(k, ""))[:255] for k in ("email", "password", "name", "surname", "country")}
            if action == "signup":
                fields["accept_terms"] = bool(body.get("accept_terms"))
                # The account's emails (the confirmation link first) are
                # written in the language the person set up in, when the
                # request said one (docs/i18n.md, "bridge"). English is not
                # sent: the apps pass "en" whenever they fall back to it, and
                # the bridge keeps "" for a language it can't tell, so a later
                # bridge carrying the person's language can still pick it.
                code, chosen = self.lang()
                if chosen and code != "en":
                    fields["lang"] = code
            try:
                status, data = account_sign_in(action, fields)
            except Exception as e:  # noqa: BLE001
                self.send_json(502, err_body("unreachable", bridge=CONFIG["bridge"], detail=str(e)))
                return
            # The bridge gives no setup code to an account whose email isn't
            # confirmed yet: the page asks the person to open the link and
            # signs in again.
            if data.get("verify_email"):
                self.send_json(202, {"verify": True,
                                     "email": (data.get("account") or {}).get("email", fields["email"])})
                return
            if status not in (200, 201) or not data.get("setup_token"):
                self.send_json(status if status >= 400 else 502, bridge_error(data, "signin_failed"))
                return
            st = update_state({"skip_bridge": False, "setup_token": data["setup_token"],
                               "account_email": (data.get("account") or {}).get("email", fields["email"])})
            self.send_json(200, {"email": st["account_email"]})

        elif path == "/api/name":
            name = str(body.get("name", "")).strip().lower()
            if not NAME_RE.match(name):
                self.send_json(400, err_body("name_rule"))
                return
            st = load_state()
            snap = install.snapshot()
            rebind = snap["phase"] == "needs_name"
            if not rebind and st.get("name") == name and st.get("domain"):
                self.send_json(200, {"domain": st["domain"]})
                return
            if not st.get("setup_token"):
                self.send_json(401, err_body("login_required"))
                return
            try:
                domain, identity = claim_name(name, st["setup_token"])
                if rebind:
                    if not CONFIG["dry_run"]:
                        rebind_recovered_device(domain, identity)
                    write_env_file(identity)
                    update_state({"name": name, "domain": domain})
                    threading.Thread(target=install.verify, daemon=True).start()
                else:
                    write_env_file(identity)
                    update_state({"name": name, "domain": domain})
            except urllib.error.URLError as e:
                self.send_json(502, err_body("unreachable", bridge=CONFIG["bridge"], detail=str(e)))
                return
            except Refused as e:
                self.send_json(409, e.answer)
                return
            except RuntimeError as e:
                self.send_json(409, {"error": str(e)})
                return
            self.send_json(200, {"domain": domain})

        elif path == "/api/local-name":
            # Issue #124: no bridge - the device is "otc" on the home
            # network; an identity is still written so install.sh has one.
            st = load_state()
            if not st.get("skip_bridge"):
                self.send_json(400, err_body("account_chosen"))
                return
            identity = new_identity()
            write_env_file(identity)
            update_state({"name": "otc", "domain": ""})
            self.send_json(200, {"domain": ""})

        elif path == "/api/install":
            st = load_state()
            mode = str(body.get("mode", "fresh"))
            disks = [str(d) for d in (body.get("disks") or [])]
            if not bridge_reachable():
                self.send_json(409, err_body("offline", bridge=CONFIG["bridge"]))
                return
            if mode == "recover":
                arr = detect_recovery()
                if not arr.get("found"):
                    self.send_json(400, err_body("no_array"))
                    return
                disks = arr["members"][:2]
                update_state({"disks": disks})
                install.start("recovery", disks, recovery=True)
            else:
                if not st.get("name"):
                    self.send_json(400, err_body("name_first"))
                    return
                # The owner password, sealed by the page (see SealKey). Kept
                # for install.sh in a root-only file on tmpfs; a retry that
                # doesn't send it again uses the one already there.
                sealed = body.get("owner_password")
                if sealed:
                    pw = SEAL.open(str(sealed))
                    try:
                        pw = pw.decode("utf-8") if pw is not None else None
                    except UnicodeDecodeError:
                        pw = None
                    if pw is None:
                        self.send_json(400, err_body("password_unreadable"))
                        return
                    if len(pw) < 8 or "\n" in pw or "\r" in pw:
                        self.send_json(400, err_body("password_too_short"))
                        return
                    pf = Path(CONFIG["owner_password_file"])
                    pf.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                    fd = os.open(str(pf), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
                    with os.fdopen(fd, "w") as fh:
                        fh.write(pw + "\n")
                # Issue #178: the owner's profile and whether face
                # recognition is on (off unless ticked) - all optional.
                prof = body.get("profile")
                if isinstance(prof, dict):
                    pname = str(prof.get("name", ""))[:100].strip()
                    ptext = str(prof.get("text", ""))[:500].strip()
                    pimg = str(prof.get("image", ""))
                    if pimg:
                        try:
                            raw = base64.b64decode(pimg, validate=True)
                        except (ValueError, TypeError):
                            raw = b""
                        # A JPEG the page cropped, small by construction.
                        if not (raw[:3] == b"\xff\xd8\xff" and len(raw) <= 96 * 1024):
                            self.send_json(400, err_body("picture_unusable"))
                            return
                    pf = Path(CONFIG["profile_file"])
                    pf.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                    fd = os.open(str(pf), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
                    with os.fdopen(fd, "w") as fh:
                        json.dump({"name": pname, "text": ptext, "image": pimg, "faces": bool(prof.get("faces"))}, fh)
                known = {d["path"] for d in list_disks()}
                if len(disks) not in (0, 2) or any(d not in known for d in disks) or len(set(disks)) != len(disks):
                    self.send_json(400, err_body("disks_choice"))
                    return
                arr = detect_recovery()
                if arr.get("found") and any(d in arr.get("members", []) for d in disks):
                    if not body.get("confirm_wipe"):
                        self.send_json(409, err_body("confirm_wipe"))
                        return
                    if not CONFIG["dry_run"]:
                        wipe_array(arr)
                update_state({"disks": disks})
                install.start(st["name"], disks, recovery=False)
            self.send_json(202, {"ok": True})

        elif path == "/api/verify":
            # "Check again" after an offline verdict.
            if install.snapshot()["phase"] in ("offline", "needs_name"):
                threading.Thread(target=install.verify, daemon=True).start()
            self.send_json(202, {"ok": True})

        elif path == "/api/finish":
            # "Open it anyway": the person takes over from here.
            if install.snapshot()["phase"] in ("offline", "needs_name"):
                install.finish()
            self.send_json(202, {"ok": True})
        else:
            self.send_json(404, err_body("not_found"))


# ---------------------------------------------------------------------------
# The page
# ---------------------------------------------------------------------------

PAGE = r"""<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title></title>
<style>
:root{--bg:#1e1f22;--panel:#2a2b30;--line:#3a3b42;--ink:#eaf2ef;--dim:#a7b0ab;--ember:#f07a5a;--ember-ink:#1e1f22;--ok:#7fd1a5;--bad:#f28b82}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:16px/1.45 -apple-system,system-ui,Segoe UI,Roboto,sans-serif}
main{max-width:560px;margin:0 auto;padding:24px 16px 48px}
h1{font-size:1.5rem;margin:8px 0 4px}p.lead{color:var(--dim);margin:0 0 20px}
ol.steps{list-style:none;padding:0;margin:0 0 20px;display:flex;gap:6px}
ol.steps li{flex:1;height:6px;border-radius:3px;background:var(--line)}ol.steps li.on{background:var(--ember)}ol.steps li.done{background:var(--ok)}
.card{background:var(--panel);border:1px solid var(--line);border-radius:14px;padding:18px}
h2{margin:0 0 6px;font-size:1.15rem}.hint{color:var(--dim);font-size:.95rem;margin:0 0 14px}
label{display:block;font-size:.9rem;color:var(--dim);margin:12px 0 4px}
input[type=text],input[type=password]{width:100%;font:inherit;padding:11px 12px;border-radius:10px;border:1px solid var(--line);background:var(--bg);color:var(--ink)}
input:focus{outline:2px solid var(--ember);border-color:transparent}
button{font:inherit;font-weight:600;padding:11px 16px;border-radius:10px;border:0;background:var(--ember);color:var(--ember-ink);cursor:pointer;margin-top:16px}
button.ghost{background:transparent;color:var(--dim);border:1px solid var(--line)}button:disabled{opacity:.5;cursor:default}
.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.row>*{margin-top:0}.row:has(>button:first-child){margin-top:16px}.row+.list{margin-top:12px}
.crop{display:flex;gap:14px;align-items:center;flex-wrap:wrap}.crop canvas{width:180px;height:180px;border-radius:12px;background:var(--bg);border:1px solid var(--line);touch-action:none;cursor:grab}
.crop .side{flex:1;min-width:160px}.crop input[type=range]{width:100%}.check{display:flex;gap:8px;align-items:center;color:var(--ink);margin-top:16px}
.prof{margin-top:28px;padding-top:20px;border-top:1px solid var(--line)}.prof h3{margin:0 0 4px}
.prof+.row{margin-top:32px;padding-top:20px;border-top:1px solid var(--line)}
ul.list{list-style:none;padding:0;margin:0;border:1px solid var(--line);border-radius:10px;overflow:hidden;max-height:300px;overflow-y:auto}
ul.list li{padding:11px 12px;border-bottom:1px solid var(--line);display:flex;justify-content:space-between;align-items:center;cursor:pointer}
ul.list li:last-child{border-bottom:0}ul.list li.sel{background:rgba(240,122,90,.15)}ul.list li small{color:var(--dim)}
.msg{margin-top:12px;font-size:.95rem}.msg.bad{color:var(--bad)}.msg.ok{color:var(--ok)}
.recover{border:1px solid var(--ok);border-radius:10px;padding:12px;margin:0 0 14px}
.bar{height:12px;background:var(--line);border-radius:6px;overflow:hidden;margin-top:14px}.bar>div{height:100%;background:var(--ember);width:0;transition:width .6s}
.step{margin-top:10px;font-weight:600}.detail{color:var(--dim);font-size:.9rem;min-height:1.3em}
pre{background:var(--bg);border:1px solid var(--line);border-radius:10px;padding:10px;font-size:.8rem;max-height:260px;overflow:auto;white-space:pre-wrap;word-break:break-all}
.spin{display:inline-block;width:14px;height:14px;border:2px solid var(--dim);border-top-color:var(--ember);border-radius:50%;animation:s 1s linear infinite;vertical-align:-2px;margin-right:6px}@keyframes s{to{transform:rotate(360deg)}}
a{color:var(--ember)}
.sso{display:flex;align-items:center;gap:12px;width:100%;margin-top:10px;padding:12px 16px;border-radius:10px;font-weight:600;font-size:1rem;cursor:pointer}
.sso svg{width:22px;height:22px;flex:none}.sso span{flex:1;text-align:center;margin-right:34px}
.sso.apple{background:#000;color:#fff;border:1px solid #5a5b62}
.sso.google,.sso.email{background:transparent;color:var(--ink);border:1px solid var(--line)}
.sso-or{display:flex;align-items:center;gap:10px;color:var(--dim);font-size:.85rem;margin:14px 0 2px}.sso-or:before,.sso-or:after{content:"";flex:1;border-top:1px solid var(--line)}
</style></head><body><main>
<h1>Off The Cloud</h1><p class="lead" id="lead"><span id="devid" hidden></span></p>
<ol class="steps"><li id="s1"></li><li id="s2"></li><li id="s3"></li><li id="s4"></li><li id="s5"></li></ol>
<div id="view" class="card"></div>
</main>
<script type="application/json" id="i18n">{}</script>
<script>
const $=s=>document.querySelector(s);const view=$('#view');
// The page's text (docs/i18n.md, "Setup wizard"): the wizard puts the
// language and its dictionaries in the page - English, and that language's
// translations, which fall back to English key by key. The language came
// from ?lang= (the apps pass theirs) or Accept-Language; every /api call
// sends it back (api below), since Bluetooth forwards the path, not the
// headers. Arguments go into a text in one pass, as plain text, and no
// translated text or argument is ever parsed as markup: the views are built
// node by node (h, rich).
const L=JSON.parse($('#i18n').textContent),EN=L.en||{},TX=L.texts||{},LQ=L.chosen?'lang='+encodeURIComponent(L.lang):'';
const has=(o,k)=>Object.prototype.hasOwnProperty.call(o,k);
// The arguments the catalog types "user" (a network's name, an email
// address): isolated, so a right-to-left value can't reorder the sentence
// around it.
const USER_ARGS=['ssid','email'];
function fill(s,a,iso){return s.replace(/\{([a-z][a-z0-9_]*)\}/g,(m,k)=>{if(!a||!has(a,k)||a[k]==null)return '';const v=String(a[k]);return iso&&USER_ARGS.includes(k)?'⁨'+v.replace(/[‪-‮⁦-⁩]/g,'')+'⁩':v})}
// A key's text in the page's language, else in English - with the plural
// rules of the language it is in (one is 1, and 0 too in French); the
// plural's number is the argument "count".
function src(k,a){const own=has(TX,k),s=own?TX[k]:has(EN,k)?EN[k]:k;if(typeof s==='string')return s;const n=Number(a&&a.count);return n===1||own&&L.lang==='fr'&&n===0?s.one:s.other}
function t(k,a){return fill(src(k,a),a,true)}
// A key's English, as the wizard writes it in its answers and the state.
function en(k,a){if(!has(EN,k))return null;const s=EN[k];return fill(typeof s==='string'?s:Number(a&&a.count)===1?s.one:s.other,a,false)}
// An English text the wizard or install.sh wrote (an error, a step) in the
// page's language: k's translation when k says exactly that text in
// English, else the text as it came.
function known(s,k,a){return s&&k&&has(TX,k)&&en(k,a||{})===s?t(k,a||{}):s}
// What a failed /api call says, in the page's language where the page knows
// the sentence (wiz.error.<code> for the wizard's own, wiz.bridge.<code> for
// the bridge's, passed on); k when it said nothing.
function errOf(r,k){if(!r.error)return t(k);for(const p of ['wiz.error.','wiz.bridge.']){const s=known(r.error,r.code&&p+r.code,r.args);if(s!==r.error)return s}return r.error}
// h(tag, attributes, ...children): an element. Children are nodes or
// strings (text nodes), in nested arrays; null, false and '' are skipped.
const kids=a=>a.flat(9).filter(x=>x!=null&&x!==false&&x!=='');
function h(tag,at,...c){const e=document.createElement(tag);for(const k in at||{}){const v=at[k];if(v==null||v===false)continue;if(k.startsWith('on'))e[k]=v;else e.setAttribute(k,v===true?'':v)}e.append(...kids(c));return e}
// rich(key, args, tags): a text with spans (<b>…</b>, <terms>…</terms>) as
// nodes. The spans are found in the text before any argument goes in; each
// tag's element comes from code (tags[name], or <b>/<i> themselves).
function rich(k,a,tags){const s=src(k,a),out=[],re=/<([a-z][a-z0-9]*)>([\s\S]*?)<\/\1>/g;let i=0,m;
 while((m=re.exec(s))){out.push(fill(s.slice(i,m.index),a,true));const c=fill(m[2],a,true),f=tags&&has(tags,m[1])?tags[m[1]]:null;out.push(f?f(c):m[1]==='b'||m[1]==='i'?h(m[1],{},c):c);i=re.lastIndex}
 out.push(fill(s.slice(i),a,true));return out}
function paint(...c){view.replaceChildren(...kids(c))}
const spin=()=>h('span',{class:'spin'});
// The logos on the sign-in buttons: constant markup of the page's own, with
// no text in it.
function svg(markup){const tp=document.createElement('template');tp.innerHTML=markup;return tp.content.firstChild}
document.documentElement.lang=L.lang;document.title=t('wiz.page.title');$('#lead').prepend(t('wiz.page.lead'));view.textContent=t('common.loading');
// Enter in a field presses the next button after it (Connect, Continue,
// Install...), as in any form; secondary buttons (.ghost) are skipped.
document.addEventListener('keydown',e=>{if(e.key!=='Enter'||e.isComposing||e.target.tagName!=='INPUT'||e.target.type==='checkbox')return;
 const b=[...view.querySelectorAll('button:not(.ghost)')].find(x=>!x.disabled&&(e.target.compareDocumentPosition(x)&Node.DOCUMENT_POSITION_FOLLOWING));
 if(b){e.preventDefault();b.click()}});
// The owner password (issue #137 follow-up): chosen with the device's name,
// sealed with the wizard's one-off RSA key before it leaves the phone - the
// hotspot is open WiFi and Bluetooth isn't paired - and handed to the app
// too, in the app, so it signs in by itself at the end.
const SHA256=(()=>{const K=[0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2];
const r=(x,n)=>(x>>>n)|(x<<(32-n));
return b=>{const l=b.length,bl=((l+9+63)>>6)<<6,m=new Uint8Array(bl);m.set(b);m[l]=0x80;const bits=l*8;m[bl-4]=bits>>>24;m[bl-3]=bits>>>16;m[bl-2]=bits>>>8;m[bl-1]=bits;m[bl-5]=Math.floor(bits/2**32);
let H=[0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19];const w=new Uint32Array(64);
for(let o=0;o<bl;o+=64){for(let i=0;i<16;i++)w[i]=(m[o+4*i]<<24)|(m[o+4*i+1]<<16)|(m[o+4*i+2]<<8)|m[o+4*i+3];
for(let i=16;i<64;i++){const s0=r(w[i-15],7)^r(w[i-15],18)^(w[i-15]>>>3),s1=r(w[i-2],17)^r(w[i-2],19)^(w[i-2]>>>10);w[i]=(w[i-16]+s0+w[i-7]+s1)|0}
let[a,b2,c,d,e,f,g,h]=H;for(let i=0;i<64;i++){const S1=r(e,6)^r(e,11)^r(e,25),ch=(e&f)^(~e&g),t1=(h+S1+ch+K[i]+w[i])|0,S0=r(a,2)^r(a,13)^r(a,22),mj=(a&b2)^(a&c)^(b2&c),t2=(S0+mj)|0;h=g;g=f;f=e;e=(d+t1)|0;d=c;c=b2;b2=a;a=(t1+t2)|0}
H=[(H[0]+a)|0,(H[1]+b2)|0,(H[2]+c)|0,(H[3]+d)|0,(H[4]+e)|0,(H[5]+f)|0,(H[6]+g)|0,(H[7]+h)|0]}
const out=new Uint8Array(32);H.forEach((v,i)=>{out[4*i]=v>>>24;out[4*i+1]=v>>>16;out[4*i+2]=v>>>8;out[4*i+3]=v});return out}})();
function mgf1(seed,len){const out=new Uint8Array(len+32);let c=0,o=0;while(o<len){const b=new Uint8Array(seed.length+4);b.set(seed);b[seed.length]=c>>>24;b[seed.length+1]=c>>>16;b[seed.length+2]=c>>>8;b[seed.length+3]=c;out.set(SHA256(b),o);o+=32;c++}return out.slice(0,len)}
function modPow(b,e,m){let r=1n;b%=m;while(e>0n){if(e&1n)r=r*b%m;b=b*b%m;e>>=1n}return r}
function sealWith(pk,pw){const n=BigInt('0x'+pk.n),e=BigInt(pk.e),k=Math.ceil(pk.n.length/2),h=32,msg=new TextEncoder().encode(pw);if(msg.length>k-2*h-2)throw new Error('password too long');
const db=new Uint8Array(k-h-1);db.set(SHA256(new Uint8Array(0)));db[db.length-msg.length-1]=1;db.set(msg,db.length-msg.length);
const seed=crypto.getRandomValues(new Uint8Array(h)),dm=mgf1(seed,db.length);for(let i=0;i<db.length;i++)db[i]^=dm[i];const sm=mgf1(db,h);for(let i=0;i<h;i++)seed[i]^=sm[i];
const em=new Uint8Array(k);em.set(seed,1);em.set(db,1+h);let x=0n;for(const v of em)x=(x<<8n)|BigInt(v);let c=modPow(x,e,n);const out=new Uint8Array(k);for(let i=k-1;i>=0;i--){out[i]=Number(c&255n);c>>=8n}
let s='';for(const v of out)s+=String.fromCharCode(v);return btoa(s)}

let owner={pw:'',pw2:''};
// Issue #178: the owner's profile and the face-recognition choice, all
// optional (Settings has them too). The picture is cropped here to a
// circle's square, a small JPEG - it travels over Bluetooth with the
// install request.
let prof={name:'',text:'',image:'',faces:false};const crop={img:null,x:0,y:0,z:1};const CROP=240,OUT=320;
function profileFields(){return h('section',{class:'prof'},h('h3',{},t('wiz.profile.title'),' ',h('span',{class:'detail'},t('wiz.profile.optional'))),
 h('label',{},t('wiz.profile.name_label')),h('input',{type:'text',id:'pn',maxlength:'100',value:prof.name,placeholder:t('wiz.profile.name_placeholder')}),
 h('label',{},t('wiz.profile.about_label')),h('input',{type:'text',id:'pt',maxlength:'500',value:prof.text,placeholder:t('wiz.profile.about_placeholder')}),
 h('label',{},t('wiz.profile.picture_label')),h('div',{class:'crop'},h('canvas',{id:'pc',width:CROP,height:CROP}),h('div',{class:'side'},h('input',{type:'file',id:'pf',accept:'image/*'}),h('label',{},t('wiz.profile.zoom')),h('input',{type:'range',id:'pz',min:'1',max:'4',step:'0.01',value:crop.z,disabled:!crop.img}),h('p',{class:'hint'},t('wiz.profile.drag_hint')))),
 h('label',{class:'check'},h('input',{type:'checkbox',id:'pfc',checked:prof.faces}),' ',t('wiz.profile.faces_label')),
 h('p',{class:'hint'},t('wiz.profile.faces_hint')),
 h('p',{class:'hint'},t('wiz.profile.optional_hint')))}
function cropScale(){const im=crop.img;return crop.z*Math.max(CROP/im.width,CROP/im.height)}
function cropClamp(){const s=cropScale(),mx=Math.max(0,(crop.img.width*s-CROP)/2),my=Math.max(0,(crop.img.height*s-CROP)/2);crop.x=Math.min(mx,Math.max(-mx,crop.x));crop.y=Math.min(my,Math.max(-my,crop.y))}
function cropPaint(ctx,size,mask){const k=size/CROP,s=cropScale()*k,w=crop.img.width*s,h=crop.img.height*s;ctx.fillStyle='#1e1f22';ctx.fillRect(0,0,size,size);ctx.drawImage(crop.img,(size-w)/2+crop.x*k,(size-h)/2+crop.y*k,w,h);
 if(mask){ctx.fillStyle='rgba(0,0,0,.55)';ctx.beginPath();ctx.rect(0,0,size,size);ctx.arc(size/2,size/2,size/2-2,0,Math.PI*2,true);ctx.fill('evenodd')}}
function cropDraw(){const c=$('#pc');if(!c)return;const ctx=c.getContext('2d');if(!crop.img){ctx.fillStyle='#1e1f22';ctx.fillRect(0,0,CROP,CROP);return}cropPaint(ctx,CROP,true)}
function cropSave(){if(!crop.img)return;const o=document.createElement('canvas');o.width=o.height=OUT;cropPaint(o.getContext('2d'),OUT,false);
 for(const q of [.85,.7,.55,.4]){const b64=o.toDataURL('image/jpeg',q).split(',')[1];if(b64.length<=60000||q===.4){prof.image=b64;return}}}
function wireProfile(){if(!$('#pn'))return;
 $('#pn').oninput=e=>{prof.name=e.target.value};$('#pt').oninput=e=>{prof.text=e.target.value};$('#pfc').onchange=e=>{prof.faces=e.target.checked};
 const c=$('#pc'),z=$('#pz');cropDraw();
 $('#pf').onchange=e=>{const f=e.target.files&&e.target.files[0];if(!f)return;const im=new Image();im.onload=()=>{crop.img=im;crop.z=1;crop.x=0;crop.y=0;z.disabled=false;z.value=1;cropDraw();cropSave()};const r=new FileReader();r.onload=()=>{im.src=r.result};r.readAsDataURL(f)};
 z.oninput=()=>{if(!crop.img)return;crop.z=+z.value;cropClamp();cropDraw();cropSave()};
 let drag=null;const k=()=>CROP/c.getBoundingClientRect().width;
 c.onpointerdown=e=>{if(!crop.img)return;drag={x:e.clientX,y:e.clientY,ox:crop.x,oy:crop.y};c.setPointerCapture(e.pointerId)};
 c.onpointermove=e=>{if(!drag)return;crop.x=drag.ox+(e.clientX-drag.x)*k();crop.y=drag.oy+(e.clientY-drag.y)*k();cropClamp();cropDraw()};
 c.onpointerup=c.onpointercancel=()=>{if(drag){drag=null;cropSave()}}}
const ownerValid=()=>owner.pw.length>=8&&owner.pw===owner.pw2;
async function installBody(){const b={mode:'fresh',disks:disks.sel,confirm_wipe:true,profile:{name:prof.name,text:prof.text,image:prof.image,faces:prof.faces}};if(owner.pw){b.owner_password=sealWith(await api('/api/pubkey'),owner.pw);if(window.otcSetupPassword)window.otcSetupPassword(owner.pw)}return b}
function ownerFields(){return [h('label',{},t('wiz.owner.password_label')),h('input',{type:'password',id:'opw',autocomplete:'new-password',value:owner.pw}),h('label',{},t('wiz.owner.repeat_label')),h('input',{type:'password',id:'opw2',autocomplete:'new-password',value:owner.pw2}),h('p',{class:'hint',id:'ohint',style:'margin-top:8px'},t('wiz.owner.hint'))]}
function wireOwner(update){const a=$('#opw'),b=$('#opw2');if(!a)return;const on=()=>{owner.pw=a.value;owner.pw2=b.value;const h=$('#ohint');const mis=owner.pw2&&owner.pw!==owner.pw2;h.textContent=mis?t('wiz.owner.mismatch'):(owner.pw&&owner.pw.length<8?t('wiz.owner.too_short'):t('wiz.owner.hint'));h.className=mis||(owner.pw&&owner.pw.length<8)?'hint bad':'hint';h.style.color=mis?'var(--bad)':'';update()};a.oninput=on;b.oninput=on}
let state=null,step=1,wifi={list:[],sel:null,joining:false},name={val:'',ok:null,domain:'',msg:''},disks={list:[],sel:[],recovery:null,loaded:false,wipe:false},acct={mode:'login',msg:'',busy:false,countries:null,email:false};
const api=async(p,o)=>{if(LQ)p+=(p.includes('?')?'&':'?')+LQ;const r=await fetch(p,Object.assign({cache:'no-store'},o||{}));let j={};try{j=await r.json()}catch(e){}return{ok:r.ok,status:r.status,...j}};
const post=(p,b)=>api(p,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(b||{})});
function marks(){for(let i=1;i<=5;i++){const li=$('#s'+i);li.className=i<step?'done':i===step?'on':''}}
let lastKey='';
async function refresh(){let st;try{st=await api('/api/state')}catch(e){return}
 if(st.device_id&&$('#devid').hidden){const bl=h('a',{href:'#',id:'blink'},t('wiz.page.blink'));$('#devid').replaceChildren(' ',...rich('wiz.page.device_id',{id:st.device_id}),' ',bl);$('#devid').hidden=false;
  bl.onclick=e=>{e.preventDefault();post('/api/identify',{}).catch(()=>{});bl.textContent=t('wiz.page.blinking');setTimeout(()=>{bl.textContent=t('wiz.page.blink')},15000)}}
 const first=state===null;state=st;
 const ph=state.install.phase;const jr=state.join_result||{};
 if(state.already_installed&&ph==='idle'){step=6}
 else if(ph!=='idle'){step=5}
 else if(step===1&&state.online&&(wifi.joining||(first&&jr.ok))){wifi.joining=false;step=2;if(!disks.loaded)loadDisks()}
 // Only touch the page when something it shows has changed - a re-render
 // while someone is typing a password throws their focus away.
 const key=JSON.stringify([step,state.online,state.ssid,jr,ph,state.install.step,state.install.detail,state.install.error,state.install.domain]);
 if(key===lastKey&&!first)return;lastKey=key;
 // A join that failed must be shown even while the joining view is up.
 if(step===1&&wifi.joining&&jr&&jr.ok===false){wifi.joining=false;render();return}
 if(typing()||(step===1&&wifi.joining&&!state.online))return;
 render()}
function typing(){const a=document.activeElement;return a&&(a.tagName==='INPUT')&&view.contains(a)&&a.value!==''}
function render(){marks();
 if(step===1)return renderWifi();if(step===2)return renderDisks();if(step===3)return renderAccount();if(step===4)return renderName(false);if(step===5)return renderInstall();
 paint(h('h2',{},t('wiz.done.title')),h('p',{class:'hint'},t('wiz.done.hint')),h('p',{},h('a',{href:'https://'+(state.domain||state.bridge)},state.domain?t('wiz.done.open',{domain:state.domain}):t('wiz.done.open_app'))))}
function renderWifi(){const online=state&&state.online;const cur=state&&state.ssid;const jr=state&&state.join_result;
 if(wifi.joining){paint(h('h2',{},t('wiz.wifi.joining_title',{ssid:wifi.sel})),
  h('div',{class:'step'},spin(),t('wiz.wifi.joining_wait')),
  window.otcApp?null:h('p',{style:'margin-top:12px;padding:10px 12px;border:1px solid var(--ember);border-radius:10px'},rich('wiz.wifi.reconnect')),
  h('p',{class:'hint'},t('wiz.wifi.keep_open')),
  h('p',{class:'hint'},t('wiz.wifi.wrong_password')));if(!window.otcApp)pollBridge();return}
 const nets=wifi.list.map(n=>{const b=n.bands||['2.4'];const only5=b.length===1&&b[0]==='5';const off=only5&&!window.otcApp;
  return h('li',{'data-ssid':off?null:n.ssid,class:wifi.sel===n.ssid?'sel':'',style:off?'opacity:.5;cursor:default':null},h('span',{},n.ssid,n.secured?' 🔒':'',off?[h('br'),h('small',{},t('wiz.wifi.only_5ghz'))]:''),h('small',{},t('wiz.wifi.bands_signal',{bands:b.join(' · '),signal:n.signal})))});
 paint(h('h2',{},t('wiz.wifi.title')),h('p',{class:'hint'},online?(cur?rich('wiz.wifi.online_via',{ssid:cur}):t('wiz.wifi.online')):t(window.otcApp?'wiz.wifi.choose_app':'wiz.wifi.choose_hotspot')),
  h('div',{class:'row'},h('button',{class:'ghost',id:'rescan',style:'margin-top:0'},t('wiz.wifi.scan_again')),h('span',{id:'scanmsg',class:'detail'})),
  h('ul',{class:'list',id:'nets'},nets.length?nets:h('li',{},h('small',{},t('wiz.wifi.none')))),
  h('label',{},t('common.password')),h('input',{type:'password',id:'pw',placeholder:wifi.sel?t('wiz.wifi.password_for',{ssid:wifi.sel}):t('wiz.wifi.pick_first'),disabled:!wifi.sel}),
  h('div',{class:'row'},h('button',{id:'join',disabled:!(wifi.sel&&!wifi.joining)},wifi.joining?[spin(),t('common.connecting')]:t('common.connect')),online?h('button',{class:'ghost',id:'skip'},t('wiz.action.continue')):null),
  h('div',{class:'msg '+(jr&&jr.ok===false?'bad':''),id:'wmsg'},jr&&jr.ok===false?[jr.error?known(jr.error,'wiz.wifi.join_rejected'):t('wiz.wifi.join_failed'),window.otcApp?'':' '+t('wiz.wifi.hotspot_back')]:''));
 $('#rescan').onclick=()=>scan(true);document.querySelectorAll('#nets li[data-ssid]').forEach(li=>li.onclick=()=>{wifi.sel=li.dataset.ssid;render();$('#pw').focus()});
 $('#join').onclick=async()=>{const pw=$('#pw').value;const net=wifi.list.find(n=>n.ssid===wifi.sel);
  if(net&&net.secured&&!pw){$('#wmsg').textContent=t('wiz.wifi.enter_password',{ssid:wifi.sel});$('#wmsg').className='msg bad';$('#pw').focus();return}
  const r=await post('/api/wifi',{ssid:wifi.sel,password:pw});if(!r.ok){$('#wmsg').textContent=errOf(r,'wiz.wifi.failed');$('#wmsg').className='msg bad';return}
  wifi.joining=true;wifi.countdown=r.delay_s||15;render();const tick=setInterval(()=>{wifi.countdown=Math.max(0,wifi.countdown-1);const c=$('#cd');if(c)c.textContent=wifi.countdown;if(wifi.countdown===0)clearInterval(tick)},1000)};
 const sk=$('#skip');if(sk)sk.onclick=()=>{step=2;if(!disks.loaded)loadDisks();render()}}
let polling=false;
async function pollBridge(){if(polling||!state||!state.token)return;polling=true;
 // The bridge is the one place both sides can reach: the device reports
 // its LAN address there once it's online, this page (still open on the
 // phone, now back on the home WiFi) picks it up and follows - in the same
 // language.
 while(polling){try{const r=await fetch(`https://${state.bridge}/api/setup-lookup?token=${encodeURIComponent(state.token)}`,{cache:'no-store'});
  if(r.ok){const j=await r.json();if(j.addr){polling=false;location.href='http://'+j.addr+'/'+(LQ?'?'+LQ:'');return}}}catch(e){}
  await new Promise(res=>setTimeout(res,2000))}}
async function scan(rescan){$('#scanmsg').replaceChildren(spin(),t('wiz.wifi.scanning'));const r=await api('/api/wifi'+(rescan?'?rescan=1':''));wifi.list=r.networks||[];render()}
async function loadDisks(){disks.loaded=true;disks.loading=true;render();const r=await api('/api/disks');disks.list=r.disks||[];disks.recovery=r.recovery&&r.recovery.found?r.recovery:null;disks.loading=false;render()}
function renderDisks(){const two=disks.sel.length===2;const rec=disks.recovery;
 if(disks.loading){paint(h('h2',{},t('wiz.disks.title')),h('p',{class:'hint'},spin(),t('wiz.disks.looking')));return}
 if(rec&&!disks.wipe){paint(h('h2',{},t('wiz.disks.title')),
  h('div',{class:'recover'},h('b',{},t('wiz.disks.found')),h('br'),h('small',{},rec.members.join(' + '),' · ',rec.size_h,rec.has_database?' · '+t('wiz.disks.with_database'):''),
   h('p',{class:'hint',style:'margin:8px 0 0'},t('wiz.disks.recover_hint'))),
  h('div',{class:'row'},h('button',{id:'recover'},t('wiz.disks.recover')),h('button',{class:'ghost',id:'wipe'},t('wiz.disks.start_fresh'))),
  h('div',{class:'msg',id:'dmsg'}));
  $('#recover').onclick=async()=>{$('#recover').disabled=true;const r=await post('/api/install',{mode:'recover'});if(!r.ok){$('#dmsg').textContent=errOf(r,'wiz.disks.start_failed');$('#dmsg').className='msg bad';$('#recover').disabled=false;return}step=4;refresh()};
  $('#wipe').onclick=()=>{disks.wipe=true;disks.sel=rec.members.slice(0,2);render()};return}
 const items=disks.list.map(d=>h('li',{'data-p':d.path,class:disks.sel.includes(d.path)?'sel':''},h('span',{},d.model||d.path,h('br'),h('small',{},d.path,' · ',d.transport||'',d.in_use?' · '+t('wiz.disks.mounted'):'')),h('small',{},d.size_h)));
 paint(h('h2',{},t('wiz.disks.title')),h('p',{class:'hint'},rich('wiz.disks.pick_hint')),
  h('ul',{class:'list',id:'dl'},items.length?items:h('li',{},h('small',{},t('wiz.disks.none')))),
  h('div',{class:'msg bad'},two?t(rec?'wiz.disks.wipe_warning_storage':'wiz.disks.wipe_warning'):disks.sel.length===1?t('wiz.disks.pick_second'):''),
  h('div',{class:'row'},h('button',{id:'go',disabled:!(disks.sel.length===0||two)},two?t('wiz.disks.wipe_continue'):t('wiz.disks.continue_without')),h('button',{class:'ghost',id:'rescan'},t('wiz.disks.rescan')),rec?h('button',{class:'ghost',id:'back'},t('common.back')):null));
 document.querySelectorAll('#dl li[data-p]').forEach(li=>li.onclick=()=>{const p=li.dataset.p;disks.sel=disks.sel.includes(p)?disks.sel.filter(x=>x!==p):disks.sel.length<2?[...disks.sel,p]:disks.sel;render()});
 $('#rescan').onclick=loadDisks;const bk=$('#back');if(bk)bk.onclick=()=>{disks.wipe=false;disks.sel=[];render()};
 $('#go').onclick=()=>{step=3;render()}}
// Issue #124: the account step. The bridge - reaching the device from
// anywhere, friends, sharing, push - needs an account that the device's
// name is registered to; the device itself works without one. Sign in or
// sign up goes through the device (the hotspot's captive DNS lets only it
// reach the bridge); Google/Apple accounts get a setup code from the
// account page on another device instead.
async function loadProviders(){if(acct.providers)return;acct.providers=[];if(!window.otcApp||!window.otcSetupSignIn)return;try{const r=await api('/api/providers');acct.providers=r.providers||[];if(step===3)render()}catch(e){}}
async function providerSignIn(p){acct.msg='';try{const tok=await window.otcSetupSignIn(p);if(!tok)return;const r=await post('/api/account',{action:'code',setup_token:tok});if(!r.ok){acct.msg=errOf(r,'wiz.account.signin_failed');render();return}acct.mode='login';await refresh();render()}catch(e){acct.msg=String(e&&e.message||e||t('wiz.account.signin_cancelled'));render()}}
function renderAccount(){const a=state.account||{};const m=acct.mode;loadProviders();
 const amsg=()=>h('div',{class:'msg '+(acct.msg?'bad':''),id:'amsg'},acct.msg);
 if(acct.verify&&!a.email){paint(h('h2',{},t('wiz.account.confirm_title')),h('p',{class:'hint'},rich('wiz.account.confirm_hint',{email:acct.verify.email})),
  h('div',{class:'row'},h('button',{id:'v-go'},t('wiz.account.confirmed')),h('button',{class:'ghost',id:'v-back'},t('common.back'))),amsg());
  $('#v-back').onclick=()=>{acct.verify=null;acct.msg='';acct.mode='login';render()};
  $('#v-go').onclick=async()=>{$('#v-go').disabled=true;$('#amsg').replaceChildren(spin(),t('wiz.account.checking'));$('#amsg').className='msg';
   const r=await post('/api/account',{action:'login',email:acct.verify.email,password:acct.verify.password});
   if(r.ok&&r.verify){acct.msg=t('wiz.account.not_confirmed');render();return}
   if(!r.ok){acct.msg=errOf(r,'wiz.account.signin_failed');render();return}
   acct.verify=null;acct.msg='';acct.mode='login';await refresh();step=4;render()};
  return}
 if(a.email&&m!=='change'){paint(h('h2',{},t('wiz.account.title')),h('p',{class:'hint'},rich('wiz.account.signed_in_as',{email:a.email})),
  h('div',{class:'row'},h('button',{id:'next'},t('wiz.action.continue')),h('button',{class:'ghost',id:'change'},t('wiz.account.use_another'))));
  $('#next').onclick=()=>{step=4;render()};$('#change').onclick=()=>{acct.mode='change';render()};return}
 const tab=(id,mode,k)=>h('button',{class:'ghost',id,style:m===mode?'border-color:var(--ember);color:var(--ink)':''},t(k));
 const tabs=h('div',{class:'row',style:'margin-bottom:6px'},tab('t-login','login','wiz.account.sign_in'),tab('t-signup','signup','wiz.account.create'),tab('t-code','code','wiz.account.have_code'));
 const terms=()=>h('div',{class:'hint',style:'border:1px solid var(--line);border-radius:10px;padding:10px 12px;margin-top:12px'},rich('wiz.account.terms_bridge',{bridge:state.bridge}),h('br'),h('br'),rich('wiz.account.terms_price'));
 const link=p=>c=>h('a',{href:'https://'+state.bridge+'/'+p,target:'_blank'},c);
 let form=[];
 if(m==='login')form=[h('label',{},t('wiz.account.email')),h('input',{type:'text',id:'a-email',autocapitalize:'none',autocomplete:'email',inputmode:'email'}),h('label',{},t('common.password')),h('input',{type:'password',id:'a-pass',autocomplete:'current-password'}),
  h('div',{class:'row'},h('button',{id:'a-go'},t('wiz.account.sign_in'))),(acct.providers||[]).length?null:h('p',{class:'hint',style:'margin-top:10px'},rich('wiz.account.provider_hint',{bridge:state.bridge}))];
 else if(m==='signup')form=[h('div',{class:'row'},h('div',{style:'flex:1'},h('label',{},t('wiz.account.name')),h('input',{type:'text',id:'a-name',autocomplete:'given-name'})),h('div',{style:'flex:1'},h('label',{},t('wiz.account.surname')),h('input',{type:'text',id:'a-surname',autocomplete:'family-name'}))),
  h('label',{},t('wiz.account.country')),h('select',{id:'a-country',style:'width:100%;font:inherit;padding:11px 12px;border-radius:10px;border:1px solid var(--line);background:var(--bg);color:var(--ink)'},h('option',{value:''},t('common.loading'))),
  h('label',{},t('wiz.account.email')),h('input',{type:'text',id:'a-email',autocapitalize:'none',autocomplete:'email',inputmode:'email'}),h('label',{},t('wiz.account.password_min')),h('input',{type:'password',id:'a-pass',autocomplete:'new-password'}),terms(),
  h('label',{style:'display:flex;gap:8px;align-items:flex-start;color:var(--ink);font-size:.95rem'},h('input',{type:'checkbox',id:'a-terms',style:'width:auto;margin-top:4px'}),h('span',{},rich('wiz.account.accept_terms',{},{terms:link('terms'),privacy:link('privacy')}))),
  h('div',{class:'row'},h('button',{id:'a-go'},t('wiz.account.create')))];
 else form=[h('label',{},t('wiz.account.code_label')),h('input',{type:'text',id:'a-code',autocapitalize:'characters',autocomplete:'off',spellcheck:'false',placeholder:t('wiz.account.code_placeholder'),style:'font-family:ui-monospace,monospace;letter-spacing:.15em;text-transform:uppercase'}),
  h('p',{class:'hint'},rich('wiz.account.code_hint',{bridge:state.bridge})),h('div',{class:'row'},h('button',{id:'a-go'},t('wiz.account.use_code')))];
 // In the app: Apple / Google as the main way in, email behind its own
 // button (issue #137) - the look sign-in pages use, Apple's logo and all.
 const APPLE='<svg viewBox="0 0 814 1000" fill="currentColor" aria-hidden="true"><path d="M788 341c-6 4-107 61-107 188 0 147 129 199 133 200-1 3-21 71-68 141-43 61-87 122-155 122s-86-40-164-40c-76 0-104 41-166 41s-106-57-155-127C49 784 0 646 0 515 0 305 137 193 271 193c72 0 131 47 176 47 43 0 110-50 192-50 31 0 143 3 149 151zM535 150c34-40 58-96 58-152 0-8-1-15-2-22-55 2-121 37-160 83-31 35-60 91-60 148 0 9 2 17 2 20 4 1 10 2 16 2 50 0 112-33 146-79z"/></svg>';
 const GOOGLE='<svg viewBox="0 0 48 48" aria-hidden="true"><path fill="#EA4335" d="M24 9.5c3.5 0 6.6 1.2 9.1 3.6l6.8-6.8C35.8 2.4 30.2 0 24 0 14.6 0 6.6 5.4 2.7 13.3l7.9 6.2C12.5 13.6 17.8 9.5 24 9.5z"/><path fill="#4285F4" d="M46.1 24.5c0-1.6-.1-3.1-.4-4.5H24v9h12.4c-.5 2.9-2.2 5.4-4.7 7.1l7.6 5.9c4.4-4.1 6.8-10.1 6.8-17.5z"/><path fill="#FBBC05" d="M10.5 28.8c-.5-1.4-.8-3-.8-4.8s.3-3.3.8-4.8l-7.9-6.2C.9 16.4 0 20.1 0 24s.9 7.6 2.6 10.9l7.9-6.1z"/><path fill="#34A853" d="M24 48c6.5 0 11.9-2.1 15.9-5.8l-7.6-5.9c-2.1 1.4-4.9 2.3-8.3 2.3-6.2 0-11.5-4.2-13.4-9.9l-7.9 6.1C6.6 42.6 14.6 48 24 48z"/></svg>';
 const EMAIL='<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M16 8v5a3 3 0 0 0 6 0v-1a10 10 0 1 0-4 8"/></svg>';
 const hasProv=(acct.providers||[]).length>0;
 const provs=hasProv?[acct.providers.slice().sort((a,b)=>a==='apple'?-1:b==='apple'?1:0).map(p=>h('button',{class:'sso '+p,'data-provider':p},svg(p==='apple'?APPLE:GOOGLE),h('span',{},t(p==='apple'?'wiz.account.continue_apple':'wiz.account.continue_google')))),acct.email?h('div',{class:'sso-or'},t('wiz.account.or_email')):h('button',{class:'sso email',id:'a-email-open'},svg(EMAIL),h('span',{},t('wiz.account.continue_email')))]:[];
 paint(h('h2',{},t('wiz.account.heading')),h('p',{class:'hint'},t('wiz.account.intro')),provs,!hasProv||acct.email?[tabs,form]:null,
  amsg(),
  h('details',{style:'margin-top:14px'},h('summary',{style:'color:var(--dim);cursor:pointer'},t('wiz.account.skip')),
   h('p',{class:'hint',style:'margin-top:8px'},t('wiz.account.skip_hint')),
   h('div',{class:'row'},h('button',{class:'ghost',id:'a-skip'},t('wiz.account.skip')))),
  h('div',{class:'row',style:'margin-top:6px'},h('button',{class:'ghost',id:'back'},t('common.back'))));
 document.querySelectorAll('button[data-provider]').forEach(b=>b.onclick=()=>providerSignIn(b.dataset.provider));
 const eo=$('#a-email-open');if(eo)eo.onclick=()=>{acct.email=true;acct.msg='';render()};
 $('#back').onclick=()=>{step=2;render()};
 $('#a-skip').onclick=async()=>{const r=await post('/api/account',{action:'skip'});if(!r.ok){acct.msg=errOf(r,'wiz.action.continue_failed');render();return}step=4;refresh();render()};
 // In the app the email options only exist once "Continue with Email" is chosen.
 if(!$('#t-login'))return;
 $('#t-login').onclick=()=>{acct.mode='login';acct.msg='';render()};$('#t-signup').onclick=()=>{acct.mode='signup';acct.msg='';render()};$('#t-code').onclick=()=>{acct.mode='code';acct.msg='';render()};
 if(m==='signup')loadCountries();
 $('#a-go').onclick=async()=>{const b=$('#a-go');b.disabled=true;$('#amsg').replaceChildren(spin(),t('wiz.account.one_moment'));$('#amsg').className='msg';let body;
  if(m==='login')body={action:'login',email:$('#a-email').value,password:$('#a-pass').value};
  else if(m==='signup')body={action:'signup',email:$('#a-email').value,password:$('#a-pass').value,name:$('#a-name').value,surname:$('#a-surname').value,country:$('#a-country').value,accept_terms:$('#a-terms').checked};
  else body={action:'code',setup_token:$('#a-code').value};
  const r=await post('/api/account',body);if(!r.ok){acct.msg=errOf(r,'wiz.account.signin_failed');render();return}
  if(r.verify){acct.verify={email:r.email||body.email,password:body.password};acct.msg='';render();return}
  acct.msg='';acct.mode='login';await refresh();step=4;render()}}
// The country list comes from the bridge, named in English; in another
// language the browser names them (Intl.DisplayNames), where it can.
async function loadCountries(){if(!acct.countries){const r=await api('/api/countries');if(r.ok){delete r.ok;delete r.status;acct.countries=r}else{$('#amsg').textContent=errOf(r,'wiz.account.countries_failed');$('#amsg').className='msg bad';return}}
 const sel=$('#a-country');if(!sel)return;const cur=sel.value;let dn=null;try{if(L.lang!=='en'&&window.Intl&&Intl.DisplayNames)dn=new Intl.DisplayNames([L.lang],{type:'region'})}catch(e){}
 const nm=(c,n)=>{if(!dn)return n;try{return dn.of(c)||n}catch(e){return n}};
 sel.replaceChildren(h('option',{value:''},t('wiz.account.choose_country')),...Object.entries(acct.countries).map(([c,n])=>[c,nm(c,n)]).sort((x,y)=>dn?x[1].localeCompare(y[1],L.lang):x[1].localeCompare(y[1])).map(([c,n])=>h('option',{value:c,selected:c===cur},n)))}
function renderName(rebind){const a=state.account||{};
 if(a.skip_bridge&&!rebind){paint(h('h2',{},t('wiz.name.local_title')),h('p',{class:'hint'},rich('wiz.name.local_hint')),
  ownerFields(),
  profileFields(),
  h('div',{class:'row'},h('button',{id:'claim',disabled:!ownerValid()},t('wiz.name.install')),h('button',{class:'ghost',id:'back'},t('common.back'))),h('div',{class:'msg',id:'nmsg'}));
  wireOwner(()=>{$('#claim').disabled=!ownerValid()});wireProfile();
  $('#back').onclick=()=>{step=3;render()};
  $('#claim').onclick=async()=>{$('#claim').disabled=true;const r=await post('/api/local-name',{});if(!r.ok){$('#nmsg').textContent=errOf(r,'wiz.action.continue_failed');$('#nmsg').className='msg bad';$('#claim').disabled=false;return}
   const i=await post('/api/install',await installBody());if(!i.ok){$('#nmsg').textContent=errOf(i,'wiz.name.install_failed');$('#nmsg').className='msg bad';$('#claim').disabled=false;return}step=5;refresh()};return}
 paint(h('h2',{},t(rebind?'wiz.name.rebind_title':'wiz.name.title')),h('p',{class:'hint'},rebind?(state.install.domain?rich('wiz.name.rebind_hint',{domain:state.install.domain}):rich('wiz.name.rebind_hint_old')):t('wiz.name.hint')),
  h('label',{},t('wiz.name.label')),h('div',{class:'row'},h('input',{type:'text',id:'nm',value:name.val,autocapitalize:'none',autocomplete:'off',spellcheck:'false',placeholder:t('wiz.name.placeholder'),style:'flex:1'}),h('span',{class:'detail',style:'white-space:nowrap'},'.'+state.bridge)),
  h('div',{class:'msg '+(name.ok===true?'ok':name.ok===false?'bad':''),id:'nmsg'},name.msg),
  rebind?null:ownerFields(),
  rebind?null:profileFields(),
  h('div',{class:'row'},h('button',{id:'claim',disabled:!(name.ok&&(rebind||ownerValid()))},t(rebind?'wiz.name.register':'wiz.action.continue')),rebind?null:h('button',{class:'ghost',id:'back'},t('common.back'))));
 nameRebind=rebind;const inp=$('#nm');inp.focus();let tm;inp.oninput=()=>{name.val=inp.value.trim().toLowerCase();name.ok=null;name.warn=false;name.msg='';clearTimeout(tm);nameStatus();if(name.val)tm=setTimeout(check,400)};
 if(!rebind){wireOwner(()=>{$('#claim').disabled=!(name.ok&&ownerValid())});wireProfile()}
 const bk=$('#back');if(bk)bk.onclick=()=>{step=3;render()};
 $('#claim').onclick=async()=>{$('#claim').disabled=true;$('#nmsg').replaceChildren(spin(),t('wiz.name.reserving'));const r=await post('/api/name',{name:name.val});
  if(!r.ok){name.ok=false;name.msg=errOf(r,'wiz.name.reserve_failed');if(r.code==='login_required'&&r.status===401){step=3;acct.msg=name.msg}render();return}
  name.domain=r.domain;if(rebind){refresh();return}
  const i=await post('/api/install',await installBody());if(!i.ok){name.msg=errOf(i,'wiz.name.install_failed');name.ok=false;render();return}step=5;refresh()}}
async function check(){const v=name.val;const r=await api('/api/name?name='+encodeURIComponent(v));if(name.val!==v)return;if(!r.ok){name.ok=false;name.msg=errOf(r,'wiz.name.check_failed')}else{name.warn=false;const d={domain:r.domain};if(r.available){name.ok=true;name.msg=t('wiz.name.available',d)}else if(r.yours===false){name.ok=false;name.msg=t('wiz.name.someone_else',d)}else if(r.yours){name.ok=true;name.warn=true;name.msg=t(r.online?'wiz.name.yours_online':'wiz.name.yours_offline',d)}else{name.ok=true;name.warn=true;name.msg=t('wiz.name.taken',d)}}nameStatus()}
// The name check's answer only changes the message and the button: the
// field itself is never redrawn, so the cursor stays where the person is
// typing (a full render put it back at the start, on Android).
let nameRebind=false;
function nameStatus(){const m=$('#nmsg'),c=$('#claim');if(!m||!c)return render();m.textContent=name.msg;m.className='msg '+(name.warn||name.ok===false?'bad':name.ok===true?'ok':'');c.disabled=!(name.ok&&(nameRebind||ownerValid()))}
const logBox=i=>h('details',{style:'margin-top:14px'},h('summary',{style:'color:var(--dim);cursor:pointer'},t('wiz.install.log')),h('pre',{},(i.log_tail||[]).join('\n')));
function renderInstall(){const i=state.install;const pct=i.total?Math.round(100*i.step/i.total):0;const dom=i.domain||state.domain||name.domain;
 const off=()=>h('p',{class:'hint'},t('wiz.ready.hotspot_off'));
 if(i.phase==='online'&&!dom){paint(h('h2',{},t('wiz.ready.title')),h('p',{class:'hint'},t('wiz.ready.local_hint')),h('p',{style:'font-size:1.2rem'},h('a',{href:'http://otc.local:8080'},h('b',{},'http://otc.local:8080'))),h('p',{class:'hint'},t('wiz.ready.local_open',{bridge:state.bridge})),off());return}
 if(i.phase==='online'&&window.otcApp){paint(h('h2',{},t('wiz.ready.title')),h('p',{class:'hint'},t(i.recovery?'wiz.ready.back':'wiz.ready.installed'),dom?[' ',rich('wiz.ready.lives_at',{domain:dom})]:null),h('p',{class:'hint'},t(i.recovery?'wiz.ready.enter_password':'wiz.ready.choose_password')));return}
 if(i.phase==='online'){paint(h('h2',{},t('wiz.ready.title')),h('p',{class:'hint'},t(i.recovery?'wiz.ready.back':'wiz.ready.installed'),' ',t('wiz.ready.from_now')),h('p',{style:'font-size:1.2rem'},h('a',{href:'https://'+dom},h('b',{},'https://'+dom))),h('p',{class:'hint'},t('wiz.ready.open_address'),i.recovery?'':' '+t('wiz.ready.owner_password')),off());return}
 if(i.phase==='needs_name'){renderName(true);return}
 if(i.phase==='verifying'){paint(h('h2',{},t('wiz.install.almost')),h('p',{class:'hint'},dom?rich('wiz.install.waiting_as',{domain:dom}):t('wiz.install.waiting')),h('div',{class:'bar'},h('div',{style:'width:100%'})),h('div',{class:'step'},spin(),t('wiz.install.checking_with',{bridge:state.bridge})));return}
 if(i.phase==='offline'){paint(h('h2',{},t('wiz.install.offline_title')),h('p',{class:'hint'},rich('wiz.install.offline_hint',{bridge:state.bridge,domain:dom})),
  h('div',{class:'row'},h('button',{id:'again'},t('wiz.install.check_again')),h('button',{class:'ghost',id:'anyway'},t('wiz.install.open_anyway'))),
  logBox(i));
  $('#again').onclick=async()=>{await post('/api/verify');refresh()};$('#anyway').onclick=async()=>{await post('/api/finish');location.href='https://'+dom};return}
 const failed=i.phase==='failed';const box='padding:10px 12px;border:1px solid var(--ok);border-radius:10px';
 paint(h('h2',{},t(i.recovery?'wiz.install.title_recovering':'wiz.install.title_installing')),h('p',{class:'hint'},t('wiz.install.hint')),
  dom&&!failed?h('p',{style:box},rich(window.otcApp?'wiz.install.app_box':'wiz.install.browser_box',{domain:dom})):null,
  h('div',{class:'bar'},h('div',{style:`width:${failed?100:pct}%;${failed?'background:var(--bad)':''}`})),
  h('div',{class:'step'},failed?t('wiz.install.failed'):[spin(),i.text?known(i.text,i.text_code&&'wiz.install.step.'+i.text_code,i.text_args):t('wiz.install.starting')],' ',h('small',{style:'color:var(--dim)'},i.step+'/'+i.total)),
  h('div',{class:'detail'},failed?(i.error?known(i.error,i.error_code&&'wiz.install.error.'+i.error_code,i.error_args):''):(i.detail||'')),
  failed?h('div',{class:'row'},h('button',{id:'retry'},t('common.try_again'))):null,
  logBox(i));
 const rt=$('#retry');if(rt)rt.onclick=async()=>{await post('/api/install',i.recovery?{mode:'recover'}:await installBody());refresh()}}
// The WiFi list on opening: in the app (Bluetooth) a fresh scan straight
// away - nothing to lose there. Over the hotspot the list saved before it
// started, since scanning takes the one radio off the hotspot and drops
// the phone's page - unless that list is empty, then a fresh one anyway.
(async()=>{await refresh();if(step===1){await scan(!!window.otcApp);if(!window.otcApp&&!wifi.list.length)await scan(true)}setInterval(refresh,3000)})();
</script></body></html>
"""


def main():
    if Path(CONFIG["install_complete_marker"]).exists() and not CONFIG["dry_run"]:
        print("[otc-setup] this device is already set up - nothing to do")
        return
    global SEAL
    SEAL = SealKey()
    Path(CONFIG["state_file"]).parent.mkdir(parents=True, exist_ok=True)
    threading.Thread(target=beacon_loop, daemon=True).start()
    # Security advisory (setup wizard): setup is over Bluetooth only - the
    # apps reach this through setup_ble.py on this machine. Listening on
    # every interface, unauthenticated, on an open hotspot let anyone
    # nearby drive a root installer (down to wiping the disks).
    bind = os.environ.get("OTC_SETUP_BIND", "127.0.0.1")
    srv = ThreadingHTTPServer((bind, CONFIG["port"]), Handler)
    print(f"[otc-setup] serving the setup wizard on port {CONFIG['port']}")
    srv.serve_forever()


if __name__ == "__main__":
    main()
