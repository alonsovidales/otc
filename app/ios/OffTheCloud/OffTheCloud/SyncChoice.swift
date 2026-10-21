// SPDX-License-Identifier: AGPL-3.0-or-later

import SwiftUI

/// What the photo sync sends from this phone, asked after every sign-in
/// to a device (Onboarding's Save & Continue and the Bluetooth setup):
/// the whole library or only what comes from now on, with the sync's own
/// switches. Until it is answered no sync runs - PhotoSync.runForeground
/// returns straight away - so a library is never uploaded to a device
/// before its owner chose to. As SyncChoice.kt on Android.
enum SyncChoice {
    /// The UserDefaults flag RootView observes (@AppStorage) to show the sheet.
    static let pendingKey = "syncChoicePending"

    static var pending: Bool { UserDefaults.standard.bool(forKey: pendingKey) }

    /// Called right before a sign-in's credentials are saved, so the sheet
    /// is up as the app opens and no sync starts first.
    static func ask() {
        UserDefaults.standard.set(true, forKey: pendingKey)
    }

    /// The answer: the same as Settings' Sync All (`all`) or Sync From Now.
    /// The sync itself is started by the caller.
    static func answer(all: Bool) {
        if all {
            UserDefaults.standard.removeObject(forKey: "lastSyncDate")
        } else {
            PhotoSync.shared.syncFromNow()
        }
        UserDefaults.standard.set(false, forKey: pendingKey)
    }
}

/// The sheet asking SyncChoice's question. It can't be swiped away: the
/// sync waits for its answer.
struct SyncChoiceView: View {
    @EnvironmentObject var secrets: SecretsStore
    /// Called once answered.
    var onDone: () -> Void
    @State private var all = true

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Text(S.appSettingsSyncChoiceIntro)
                }
                Section {
                    choiceRow(title: S.appSettingsSyncChoiceAll, detail: S.appSettingsSyncChoiceAllDetail, selected: all) { all = true }
                    choiceRow(title: S.appSettingsSyncChoiceNew, detail: S.appSettingsSyncChoiceNewDetail, selected: !all) { all = false }
                }
                Section(
                    header: Text(S.appSettingsSyncChoiceWarning).textCase(nil),
                    footer: Text(S.appSettingsSyncChoiceFooter)
                ) {
                    Toggle(S.appSettingsSyncWifiOnly, isOn: $secrets.wifiOnly)
                    Toggle(S.appSettingsSyncVideos, isOn: $secrets.includeVideos)
                    Toggle(S.iosSettingsSyncIcloud, isOn: $secrets.downloadFromiCloud)
                }
                // Asked here too, off unless turned on: a new device's first
                // sync is when a phone full of photos gets its copies.
                FreeUpSpaceSection()
                Section {
                    Button {
                        secrets.persist()
                        SyncChoice.answer(all: all)
                        onDone()
                    } label: {
                        Text(S.appSettingsSyncChoiceStart).frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets())
                }
            }
            .navigationTitle(S.appSettingsSyncChoiceTitle)
        }
        .interactiveDismissDisabled()
        // An iPad's form sheet cut Start Sync off at its foot: a page sheet
        // is tall enough for all of it.
        .presentationSizing(.page)
    }

    private func choiceRow(title: String, detail: String, selected: Bool, pick: @escaping () -> Void) -> some View {
        Button(action: pick) {
            HStack(alignment: .firstTextBaseline, spacing: 12) {
                Image(systemName: selected ? "checkmark.circle.fill" : "circle")
                    .foregroundStyle(selected ? Color.accentColor : Color.secondary)
                VStack(alignment: .leading, spacing: 2) {
                    Text(title).foregroundStyle(.primary)
                    Text(detail).font(.footnote).foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? [.isSelected] : [])
    }
}
