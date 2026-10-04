# App Store screenshots

`screenshots-6.9/` holds the iPhone screenshots for App Store Connect, at the 6.9" size Apple requires
(1320 × 2868, iPhone 18 Pro Max simulator, clock at 9:41, full battery). They were taken against
Pit, whose photos show people who agreed to it. The website's phone images
(`bridge/static/img/mobile-*.jpg`) are copies of them, and its web images (`img/web-*.jpg`, with
the originals in `docs/screenshots/web/`) come from the web app at 1400 × 900, 2×.

The Play Store wants Android screenshots with a ratio of at most 2:1; those are in
`docs/play-store/screenshots/` (taken on the Fold).

Retaking them: see "the iOS simulator" in CLAUDE.md (idb, `simctl status_bar … override --time 9:41`).
Never give the simulator photo access while it is signed in to a device: its sample photos would be
uploaded.
