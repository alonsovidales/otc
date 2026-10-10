// Fixture: the macOS surface.
import AppKit

func confirmDisconnect() {
    let alert = NSAlert()
    alert.messageText = "Disconnect from the device?"
    alert.informativeText = "Your synced folders are removed from this Mac."
    alert.addButton(withTitle: "Disconnect")
    let panel = NSOpenPanel()
    panel.prompt = "Choose"
    NSLog("Disconnect asked by the user")
    let raw = #"A raw "quoted" sentence here."#
    _ = raw
    let url = URL(string: "https://off-the.cloud/account")
    _ = url
}
