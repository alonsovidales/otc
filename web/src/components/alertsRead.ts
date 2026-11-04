// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Alerts marks every alert read as it opens (issue #78's design), and the
// device can only mark them all (MarkNotificationsAcknowledged takes
// nothing). It used to do that right after listing the newest 50, so an
// alert past the first 50, or one that arrived while the list was on its
// way, was marked read without ever being on screen. Now the page first
// lists as many as are unread (alertsLimit: the device's unread count
// against what the list shows), so what it marks read is on the page -
// all but what comes in the moment between the last look and the mark.
// Tested by web/scripts/alerts-read-test.mjs.

/** The newest alerts asked for as the page opens. */
export const cAlertsFirst = 50;
/** The most it lists to have every unread one on screen. */
export const cAlertsMost = 500;

/** How many alerts to list so every one unread on the device is on the
 *  page - `listed` shown, `unreadListed` of them unread, `unreadNow` the
 *  device's count - or null when they all are already, or no more could
 *  be shown. */
export function alertsLimit(listed: number, unreadListed: number, unreadNow: number, most = cAlertsMost): number | null {
  if (unreadNow <= unreadListed) return null;
  const want = Math.min(most, listed + (unreadNow - unreadListed));
  return want > listed ? want : null;
}
