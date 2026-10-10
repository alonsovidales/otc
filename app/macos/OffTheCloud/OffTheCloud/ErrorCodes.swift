// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation

/// Codes before prose (docs/i18n.md): the app acts on a reply's error_code
/// and reads its message only when it has no code. A device from before the
/// code existed says it in English alone; a localized device writes the
/// message in the owner's language, so a message that comes with a code is
/// never matched. otc-sync's engine.unknownPayload is the same.
enum ErrorCodes {
    /// The device is older than the request. Builds before v9 send only the
    /// message, "unknown payload".
    static func isUnknownPayload(code: String, message: String) -> Bool {
        code == "unknown_payload" || (code.isEmpty && message.contains("unknown payload"))
    }
}
