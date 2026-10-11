// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI

/// Settings' Language picker (docs/i18n.md, "The stored choice"):
/// Automatic, with the language it gives ("Automatic (Español)"), then each
/// language by its own name - names are never translated. The choice is
/// the user's for every Off The Cloud app, kept on the device (see
/// LanguageSettings); otc-sync's tray has the same as its Language submenu.
///
/// Shown only in a build with more than one language: while English is the
/// only one that ships there is nothing to choose. A menu inside the
/// popover, never an .alert, which would close it.
struct LanguagePicker: View {
    @ObservedObject private var language = LanguageSettings.shared

    var body: some View {
        if LanguageSettings.choosable {
            Picker(selection: Binding(get: { language.shownChoice }, set: { language.choose($0) })) {
                Text(S.deskMainLanguageAutomatic(language: language.automaticName)).tag("")
                Divider()
                ForEach(L10nLanguage.all) { l in
                    Text(verbatim: l.name).tag(l.code)
                }
            } label: {
                Text(S.deskMainLanguage)
            }
            .pickerStyle(.menu)
            .controlSize(.small)
        }
    }
}
