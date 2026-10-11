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
