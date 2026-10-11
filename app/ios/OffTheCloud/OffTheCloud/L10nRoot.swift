// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  L10nRoot.swift
//  OffTheCloud
//
//  The one view around the scene's root (docs/i18n.md, "Each platform"):
//  it puts the app's formatting locale in the environment, so SwiftUI's own
//  formatting - dates and numbers in a Text, a DatePicker, numbers in a
//  LocalizedStringKey - follows the app's language rather than the
//  system's. Reading L10n.shared.locale here makes the root draw again when
//  the language changes; every body that reads an accessor follows by
//  itself. Never .id(): it would throw away the views' state (spike S1).
//  A root view, never a Scene modifier, which macOS ignores.
//

import SwiftUI

struct L10nRoot<Content: View>: View {
    @ViewBuilder var content: Content

    var body: some View {
        content.environment(\.locale, L10n.shared.locale)
    }
}
