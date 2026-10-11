// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  LanguageView.swift
//  OffTheCloud
//
//  Settings > Language (docs/i18n.md, "The stored choice"): Automatic -
//  with the language it gives - and every language of this build by its own
//  name, which is never translated. A pick switches the app at once and is
//  kept on the device for every app (LanguageSettings). Offered only while
//  the build has more than one language (LanguageSettings.pickerShown).
//  Android's ui/settings/LanguageView.kt is the same.
//
//  LanguageMenu is the same list on the screens with no device signed in
//  to, where Settings can't be reached: the welcome screen, the Bluetooth
//  setup, and the "can't connect" and "isn't available" cards.
//

import SwiftUI

struct LanguageView: View {
    @ObservedObject private var settings = LanguageSettings.shared

    var body: some View {
        List {
            Section {
                row(code: "", title: S.appSettingsLanguageAutomaticNamed(language: settings.name(of: settings.systemLanguage)))
                ForEach(settings.languages) { language in
                    row(code: language.code, title: language.name)
                }
            } footer: {
                VStack(alignment: .leading, spacing: 8) {
                    Text(S.appSettingsLanguageFooter)
                    if settings.deviceKeepsIt == false {
                        Text(S.appSettingsLanguageThisPhone)
                    }
                }
            }
        }
        .navigationTitle(S.appSettingsLanguage)
        .navigationBarTitleDisplayMode(.inline)
    }

    private func row(code: String, title: String) -> some View {
        let selected = settings.selected == code
        return Button {
            if !selected { settings.choose(code) }
        } label: {
            HStack {
                Text(title)
                Spacer()
                if selected {
                    Image(systemName: "checkmark")
                        .foregroundStyle(Color.accentColor)
                }
            }
            .contentShape(Rectangle())
        }
        // Rows read as a list, as the system's own language lists do: only
        // the checkmark carries the accent.
        .tint(.primary)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// A globe and the name of the language the app shows, opening Settings'
/// list (Automatic with the language it gives, then each language by its
/// own name) - for the screens with no device signed in to: the welcome
/// screen, the Bluetooth setup, the "can't connect" and "isn't available"
/// cards (docs/i18n.md, "The stored choice": a pick there is shown at once
/// and goes to the device at the next sign-in). Its name for VoiceOver is
/// "Language". Hidden while English is the only language that ships.
/// Android has the same, LanguageMenu.
struct LanguageMenu: View {
    @ObservedObject private var settings = LanguageSettings.shared

    var body: some View {
        if settings.pickerShown {
            // The language on screen, by its own name.
            let shown = L10n.shared.current.language.name
            Menu {
                Picker(selection: Binding(get: { settings.selected }, set: { code in
                    if code != settings.selected { settings.choose(code) }
                })) {
                    Text(S.appSettingsLanguageAutomaticNamed(language: settings.name(of: settings.systemLanguage))).tag("")
                    ForEach(settings.languages) { language in
                        Text(verbatim: language.name).tag(language.code)
                    }
                } label: {
                    Text(S.appSettingsLanguage)
                }
                .pickerStyle(.inline)
            } label: {
                // Both, also in a toolbar (which draws a Label's icon only).
                HStack(spacing: 5) {
                    Image(systemName: "globe")
                    Text(verbatim: shown)
                }
                .fixedSize()
            }
            .accessibilityLabel(S.appSettingsLanguage)
            .accessibilityValue(Text(verbatim: shown))
        }
    }
}
