// Fixture: the otc-sync surface (the tray).
package tray

import (
	"fyne.io/systray"
	"github.com/ncruces/zenity"
)

const autostartTitle = "Start Off The Cloud when you log in?"

func build() {
	systray.AddMenuItem("Open Web App", "")
	systray.AddMenuItem("Quit", "Quit the app")
	systray.SetTooltip("Off The Cloud")
	_ = zenity.Question("Remove this folder?", zenity.Title("Off The Cloud"), zenity.OKLabel("Remove"), zenity.CancelLabel("Cancel"))
}
