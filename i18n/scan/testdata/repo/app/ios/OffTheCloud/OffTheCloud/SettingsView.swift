// SPDX-License-Identifier: AGPL-3.0-or-later
// Fixture: the iOS surface. /* "Not this, a comment" */
import SwiftUI
import os

private let log = Logger(subsystem: "cloud.offthe.otc", category: "Settings")

enum ImagesText {
    static let keep = "Keep Out of Images"
    static let key = "settings.viewMode"
}

struct SettingsView: View {
    @AppStorage("files.viewMode") private var viewMode = "list"
    @State private var alertMessage = ""
    @State private var name = ""
    let count: Int

    var body: some View {
        NavigationStack {
            Form {
                Section("Storage") {
                    Text("Used \(count) of the disk")
                    Text(verbatim: "OTC-\(count)")
                    Text("OTC")
                    Label("Photos", systemImage: "photo.on.rectangle")
                    Toggle("Face recognition", isOn: .constant(true))
                    TextField("Name", text: $name)
                    Text(count == 1 ? "One photo" : "\(count) photos")
                    Image(systemName: "trash")
                }
                Button("Delete", role: .destructive) { delete() }
                    .accessibilityLabel("Delete the folder")
                Text("Kept for the record") // i18n-ignore: fixture for the escape hatch
            }
            .navigationTitle("Settings")
            .alert("Couldn't save", isPresented: .constant(false)) {
                Button("OK") {}
            } message: {
                Text(alertMessage)
            }
        }
    }

    func delete() {
        log.error("Delete failed for the folder")
        print("Deleting the folder now")
        if name == "Some Name Here" { return }
        alertMessage = "Could not delete the folder."
        let message = """
            Everything in it is removed
            from the device.
            """
        _ = message
        switch name {
        case "Spaced Out Value": break
        default: break
        }
        let dict = ["Header Key Name": 1]
        _ = dict
        throw OTCError("The device did not answer in time")
    }
}
