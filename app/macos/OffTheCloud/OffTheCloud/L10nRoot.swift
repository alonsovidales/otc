// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI

/// The root of each scene's content (docs/i18n.md, "Each platform"): the
/// environment's locale follows the app's language, so the dates and
/// numbers SwiftUI formats by itself (Text(date, style:), DatePicker) are
/// in it too. Reading L10n.shared.locale here registers an Observation
/// dependency, so a change of language re-renders the scene in place - no
/// relaunch and no .id(), which would throw away the views' state.
///
/// It wraps the content of each scene, MenuBarExtra's PopoverView and the
/// setup Window's SetupWizardView: an .environment on the Scene itself
/// never reaches that content on macOS (spike S1).
struct L10nRoot<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        content.environment(\.locale, L10n.shared.locale)
    }
}
