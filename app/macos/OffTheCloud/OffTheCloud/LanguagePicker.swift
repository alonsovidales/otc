// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI

/// Settings' Language picker (docs/i18n.md, "The stored choice"):
/// Automatic, with the language it gives ("Automatic (Español)"), then each
/// language by its own name - names are never translated. The choice is
/// the user's for every Off The Cloud app, kept on the device (see
/// LanguageSettings); otc-sync's tray has the same as its Language submenu.
/// With no device set up (the connect form) it is kept on this Mac and goes
/// to the device this Mac signs in to next.
///
/// Shown only in a build with more than one language: while English is the
/// only one that ships there is nothing to choose. A menu inside the
/// popover, never an .alert, which would close it.
struct LanguagePicker: View {
    @ObservedObject private var language = LanguageSettings.shared

    var body: some View {
        if LanguageSettings.choosable {
            Picker(selection: Binding(get: { language.shownChoice }, set: { language.choose($0) })) {
                LanguageChoices(automaticName: language.automaticName)
            } label: {
                Text(S.deskMainLanguage)
            }
            .pickerStyle(.menu)
            .controlSize(.small)
            // The size "Start at login" shows at, above it.
            .font(.subheadline)
        }
    }
}

/// The setup wizard's language menu (docs/i18n.md, "Setup wizard"): a
/// globe and the name of the language the app shows, opening the same list
/// as Settings' picker - someone setting up a device has no Settings to go
/// to. Its tooltip and its name for VoiceOver are "Language". Hidden, like
/// the picker, while English is the only language that ships.
struct LanguageMenu: View {
    /// Told of each pick (the wizard carries it to the device it sets up).
    var onChoose: (String) -> Void = { _ in }
    @ObservedObject private var language = LanguageSettings.shared

    var body: some View {
        if LanguageSettings.choosable {
            // The language on screen, by its own name.
            let shown = L10n.shared.current.language.name
            Menu {
                Picker(selection: Binding(get: { language.shownChoice }, set: { code in
                    language.choose(code)
                    onChoose(code)
                })) {
                    LanguageChoices(automaticName: language.automaticName)
                } label: {
                    Text(S.deskMainLanguage)
                }
                .pickerStyle(.inline)
                .labelsHidden()
            } label: {
                Label {
                    Text(verbatim: shown)
                } icon: {
                    Image(systemName: "globe")
                }
                .labelStyle(.titleAndIcon)
            }
            .menuStyle(.borderlessButton)
            .controlSize(.small)
            .fixedSize()
            .help(S.deskMainLanguage)
            .accessibilityLabel(S.deskMainLanguage)
            .accessibilityValue(Text(verbatim: shown))
        }
    }
}

/// The list both offer: Automatic with the language it gives, then each
/// language by its own name.
private struct LanguageChoices: View {
    let automaticName: String

    var body: some View {
        Text(S.deskMainLanguageAutomatic(language: automaticName)).tag("")
        Divider()
        ForEach(L10nLanguage.all) { l in
            Text(verbatim: l.name).tag(l.code)
        }
    }
}
