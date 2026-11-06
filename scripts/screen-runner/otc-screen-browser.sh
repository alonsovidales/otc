#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Off The Cloud on a connected screen: the web app in its screen mode,
# full-screen in Chromium inside cage (a Wayland compositor that shows one
# app and nothing else), as the otc-screen user on tty7. Run by
# otc-screen-browser.service, which otc-screen-watch starts when a screen is
# plugged in and stops once it's gone; OTC_SCREEN_URL is the watcher's
# (/run/otc-screen/browser.env), Chromium's policies too
# (/etc/chromium/policies/managed/otc-screen.json).
#
#   otc-screen-browser --wait   as root, first: waits for the device's web
#                               app to answer (the device starting, an
#                               update restarting it) with the console on
#                               the screen, then shows tty7
#   otc-screen-browser          the browser
set -u

url="${OTC_SCREEN_URL:-}"
if ! [[ "$url" =~ ^http://127\.0\.0\.1:[0-9]{1,5}/\?screen=1$ ]]; then
    echo "otc-screen-browser: no address for the device's web app (OTC_SCREEN_URL)" >&2
    exit 1
fi

if [ "${1:-}" = "--wait" ]; then
    # Any answer is the web app up (the device without its storage serves
    # its own page on the same port).
    until curl -sS -o /dev/null --max-time 3 "$url" 2>/dev/null; do
        sleep 2
    done
    chvt 7 2>/dev/null || true
    exit 0
fi

profile="$HOME/chromium"
# A Chromium that didn't close itself (a power cut, the screen unplugged)
# offers to restore its pages the next time: there is nothing to restore.
prefs="$profile/Default/Preferences"
if [ -f "$prefs" ]; then
    sed -i -e 's/"exited_cleanly":false/"exited_cleanly":true/' \
        -e 's/"exit_type":"[^"]*"/"exit_type":"Normal"/' "$prefs" 2>/dev/null || true
fi

# cage: -d no window decorations, -s the console still reachable with
# Ctrl+Alt+F1 (a keyboard plugged in). Chromium: the kiosk (no address bar,
# no tabs, no way out), no first-run or crash bubbles, no swipe back (the
# web app has its own back), no page zoom by pinching (the viewer zooms
# photos itself), touch events on, and the profile in its own home.
exec cage -d -s -- chromium \
    --kiosk \
    --ozone-platform=wayland \
    --user-data-dir="$profile" \
    --no-first-run \
    --noerrdialogs \
    --disable-infobars \
    --disable-session-crashed-bubble \
    --disable-features=Translate,MediaRouter \
    --password-store=basic \
    --overscroll-history-navigation=0 \
    --disable-pinch \
    --touch-events=enabled \
    --check-for-update-interval=31536000 \
    "$url"
