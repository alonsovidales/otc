// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import cloud.offthe.otc.i18n.EnglishStrings
import cloud.offthe.otc.i18n.RichText
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.UiTextFormat
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.i18n.androidSettingsBridgeLeaveDelete
import cloud.offthe.otc.i18n.androidSettingsCropHint
import cloud.offthe.otc.i18n.androidSettingsCropZoom
import cloud.offthe.otc.i18n.androidSettingsFolderOnVolume
import cloud.offthe.otc.i18n.androidSettingsFolders
import cloud.offthe.otc.i18n.androidSettingsFoldersAll
import cloud.offthe.otc.i18n.androidSettingsFoldersEmpty
import cloud.offthe.otc.i18n.androidSettingsFoldersFooter
import cloud.offthe.otc.i18n.androidSettingsFoldersHidden
import cloud.offthe.otc.i18n.androidSettingsFoldersIntro
import cloud.offthe.otc.i18n.androidSettingsFoldersLoadFailed
import cloud.offthe.otc.i18n.androidSettingsFoldersOff
import cloud.offthe.otc.i18n.androidSettingsLinksDeleteText
import cloud.offthe.otc.i18n.androidSettingsLinksEmptyText
import cloud.offthe.otc.i18n.androidSettingsLogsEmpty
import cloud.offthe.otc.i18n.androidSettingsLogsFooter
import cloud.offthe.otc.i18n.androidSettingsLogsLoadFailed
import cloud.offthe.otc.i18n.androidSettingsLogsSendFailed
import cloud.offthe.otc.i18n.androidSettingsLogsSendTitle
import cloud.offthe.otc.i18n.androidSettingsPickerEmpty
import cloud.offthe.otc.i18n.androidSettingsPickerLoadFailed
import cloud.offthe.otc.i18n.androidSettingsPickerOpenFailed
import cloud.offthe.otc.i18n.androidSettingsProfileDescription
import cloud.offthe.otc.i18n.androidSettingsProfileFromPhone
import cloud.offthe.otc.i18n.androidSettingsSharingFooter
import cloud.offthe.otc.i18n.androidSettingsSyncCloud
import cloud.offthe.otc.i18n.androidSettingsSyncTitle
import cloud.offthe.otc.i18n.androidSettingsThumbsChangeSize
import cloud.offthe.otc.i18n.androidSettingsThumbsMaxSizeA11y
import cloud.offthe.otc.i18n.androidSettingsThumbsSelected
import cloud.offthe.otc.i18n.androidSettingsThumbsTitle
import cloud.offthe.otc.i18n.androidSettingsUpdateVersionUnknown
import cloud.offthe.otc.i18n.androidSettingsUploadPending
import cloud.offthe.otc.i18n.androidSettingsUploadWaiting
import cloud.offthe.otc.i18n.androidSettingsUploading
import cloud.offthe.otc.i18n.appSettingsBridgeAccountPage
import cloud.offthe.otc.i18n.appSettingsBridgeAtHome
import cloud.offthe.otc.i18n.appSettingsBridgeDeleteAccount
import cloud.offthe.otc.i18n.appSettingsBridgeDeleteMessage
import cloud.offthe.otc.i18n.appSettingsBridgeDeleteTitle
import cloud.offthe.otc.i18n.appSettingsBridgeFooter
import cloud.offthe.otc.i18n.appSettingsBridgeLeave
import cloud.offthe.otc.i18n.appSettingsBridgeLeaveConfirm
import cloud.offthe.otc.i18n.appSettingsBridgeLeaveFailed
import cloud.offthe.otc.i18n.appSettingsBridgeLeaveMessage
import cloud.offthe.otc.i18n.appSettingsBridgeLeaveTitle
import cloud.offthe.otc.i18n.appSettingsBridgeLeft
import cloud.offthe.otc.i18n.appSettingsBridgeLocalOnly
import cloud.offthe.otc.i18n.appSettingsBridgeReachable
import cloud.offthe.otc.i18n.appSettingsBridgeReleased
import cloud.offthe.otc.i18n.appSettingsBridgeTitle
import cloud.offthe.otc.i18n.appSettingsConnectionDeviceId
import cloud.offthe.otc.i18n.appSettingsConnectionLogOut
import cloud.offthe.otc.i18n.appSettingsConnectionLogOutConfirm
import cloud.offthe.otc.i18n.appSettingsConnectionLogOutMessage
import cloud.offthe.otc.i18n.appSettingsConnectionLogOutTitle
import cloud.offthe.otc.i18n.appSettingsConnectionSave
import cloud.offthe.otc.i18n.appSettingsConnectionTitle
import cloud.offthe.otc.i18n.appSettingsCropUse
import cloud.offthe.otc.i18n.appSettingsErrorSetting
import cloud.offthe.otc.i18n.appSettingsFoldersNoAccess
import cloud.offthe.otc.i18n.appSettingsFoldersPhotos
import cloud.offthe.otc.i18n.appSettingsFoldersPhotosVideos
import cloud.offthe.otc.i18n.appSettingsFoldersVideos
import cloud.offthe.otc.i18n.appSettingsLimitedAllowAll
import cloud.offthe.otc.i18n.appSettingsLimitedSelectMore
import cloud.offthe.otc.i18n.appSettingsLimitedWarning
import cloud.offthe.otc.i18n.appSettingsErrorUnexpected
import cloud.offthe.otc.i18n.appSettingsErrorUpdateFailed
import cloud.offthe.otc.i18n.appSettingsFacesFooter
import cloud.offthe.otc.i18n.appSettingsFacesTitle
import cloud.offthe.otc.i18n.appSettingsFacesToggle
import cloud.offthe.otc.i18n.appSettingsLinksCreated
import cloud.offthe.otc.i18n.appSettingsLinksDeleteFailed
import cloud.offthe.otc.i18n.appSettingsLinksDeleteLink
import cloud.offthe.otc.i18n.appSettingsLinksDeleteTitle
import cloud.offthe.otc.i18n.appSettingsLinksEmpty
import cloud.offthe.otc.i18n.appSettingsLinksExpires
import cloud.offthe.otc.i18n.appSettingsLinksLoadFailed
import cloud.offthe.otc.i18n.appSettingsLinksNeverOpened
import cloud.offthe.otc.i18n.appSettingsLinksOpened
import cloud.offthe.otc.i18n.appSettingsLinksOpenedLast
import cloud.offthe.otc.i18n.appSettingsLinksSizeFiles
import cloud.offthe.otc.i18n.appSettingsLinksTitle
import cloud.offthe.otc.i18n.appSettingsLinksUntitledGallery
import cloud.offthe.otc.i18n.appSettingsLinksUntitledZip
import cloud.offthe.otc.i18n.appSettingsLogsDevice
import cloud.offthe.otc.i18n.appSettingsLogsNote
import cloud.offthe.otc.i18n.appSettingsLogsSendExplain
import cloud.offthe.otc.i18n.appSettingsLogsSendToUs
import cloud.offthe.otc.i18n.appSettingsLogsSending
import cloud.offthe.otc.i18n.appSettingsLogsSent
import cloud.offthe.otc.i18n.appSettingsLogsTitle
import cloud.offthe.otc.i18n.appSettingsLogsUpdates
import cloud.offthe.otc.i18n.appSettingsLogsWarning
import cloud.offthe.otc.i18n.appSettingsPasswordChange
import cloud.offthe.otc.i18n.appSettingsPasswordChanged
import cloud.offthe.otc.i18n.appSettingsPasswordChanging
import cloud.offthe.otc.i18n.appSettingsPasswordConfirm
import cloud.offthe.otc.i18n.appSettingsPasswordCurrent
import cloud.offthe.otc.i18n.appSettingsPasswordError
import cloud.offthe.otc.i18n.appSettingsPasswordFailed
import cloud.offthe.otc.i18n.appSettingsPasswordFillAll
import cloud.offthe.otc.i18n.appSettingsPasswordMismatch
import cloud.offthe.otc.i18n.appSettingsPasswordNew
import cloud.offthe.otc.i18n.appSettingsPasswordNoKey
import cloud.offthe.otc.i18n.appSettingsPasswordTitle
import cloud.offthe.otc.i18n.appSettingsPickerAll
import cloud.offthe.otc.i18n.appSettingsPickerTitle
import cloud.offthe.otc.i18n.appSettingsProfileChangePhoto
import cloud.offthe.otc.i18n.appSettingsProfileFromDevice
import cloud.offthe.otc.i18n.appSettingsProfileName
import cloud.offthe.otc.i18n.appSettingsProfileSave
import cloud.offthe.otc.i18n.appSettingsProfileSaveError
import cloud.offthe.otc.i18n.appSettingsProfileSaveFailed
import cloud.offthe.otc.i18n.appSettingsProfileSaved
import cloud.offthe.otc.i18n.appSettingsProfileTitle
import cloud.offthe.otc.i18n.appSettingsReprocessAll
import cloud.offthe.otc.i18n.appSettingsReprocessCompleted
import cloud.offthe.otc.i18n.appSettingsReprocessConfirmRestart
import cloud.offthe.otc.i18n.appSettingsReprocessConfirmResume
import cloud.offthe.otc.i18n.appSettingsReprocessFailed
import cloud.offthe.otc.i18n.appSettingsReprocessFooter
import cloud.offthe.otc.i18n.appSettingsReprocessRestartButton
import cloud.offthe.otc.i18n.appSettingsReprocessResume
import cloud.offthe.otc.i18n.appSettingsReprocessResumeButton
import cloud.offthe.otc.i18n.appSettingsReprocessRunning
import cloud.offthe.otc.i18n.appSettingsReprocessStartError
import cloud.offthe.otc.i18n.appSettingsReprocessStartFailed
import cloud.offthe.otc.i18n.appSettingsReprocessStartOver
import cloud.offthe.otc.i18n.appSettingsReprocessStarting
import cloud.offthe.otc.i18n.appSettingsReprocessStopError
import cloud.offthe.otc.i18n.appSettingsReprocessStopFailed
import cloud.offthe.otc.i18n.appSettingsReprocessStopped
import cloud.offthe.otc.i18n.appSettingsReprocessStopping
import cloud.offthe.otc.i18n.appSettingsReprocessTitle
import cloud.offthe.otc.i18n.appSettingsSharingTitle
import cloud.offthe.otc.i18n.appSettingsStatusCpuMem
import cloud.offthe.otc.i18n.appSettingsStatusDisk
import cloud.offthe.otc.i18n.appSettingsStatusRaid
import cloud.offthe.otc.i18n.appSettingsStatusRaidDegraded
import cloud.offthe.otc.i18n.appSettingsStatusRaidInSync
import cloud.offthe.otc.i18n.appSettingsStatusRaidLevel
import cloud.offthe.otc.i18n.appSettingsStatusRaidNone
import cloud.offthe.otc.i18n.appSettingsStatusRaidSyncing
import cloud.offthe.otc.i18n.appSettingsStatusRaidUnknown
import cloud.offthe.otc.i18n.appSettingsStatusRaidUsed
import cloud.offthe.otc.i18n.appSettingsStatusTitle
import cloud.offthe.otc.i18n.appSettingsStatusUsedPercent
import cloud.offthe.otc.i18n.appSettingsSyncAll
import cloud.offthe.otc.i18n.appSettingsSyncAuthorize
import cloud.offthe.otc.i18n.appSettingsSyncFooter
import cloud.offthe.otc.i18n.appSettingsSyncFromNow
import cloud.offthe.otc.i18n.appSettingsSyncTitle
import cloud.offthe.otc.i18n.appSettingsSyncVideos
import cloud.offthe.otc.i18n.appSettingsSyncWifiOnly
import cloud.offthe.otc.i18n.appSettingsTaggingFooter
import cloud.offthe.otc.i18n.appSettingsTaggingTitle
import cloud.offthe.otc.i18n.appSettingsTailscaleAuthKey
import cloud.offthe.otc.i18n.appSettingsTailscaleAuthorise
import cloud.offthe.otc.i18n.appSettingsTailscaleChangeFailed
import cloud.offthe.otc.i18n.appSettingsTailscaleDone
import cloud.offthe.otc.i18n.appSettingsTailscaleEnable
import cloud.offthe.otc.i18n.appSettingsTailscaleIntro
import cloud.offthe.otc.i18n.appSettingsTailscaleKeyHint
import cloud.offthe.otc.i18n.appSettingsTailscaleLimitAccount
import cloud.offthe.otc.i18n.appSettingsTailscaleLimitBandwidth
import cloud.offthe.otc.i18n.appSettingsTailscaleLimitOneUser
import cloud.offthe.otc.i18n.appSettingsTailscaleLimitSocial
import cloud.offthe.otc.i18n.appSettingsTailscaleNoFunnel
import cloud.offthe.otc.i18n.appSettingsTailscaleNotInstalled
import cloud.offthe.otc.i18n.appSettingsTailscaleNotInstalledOn
import cloud.offthe.otc.i18n.appSettingsTailscaleOff
import cloud.offthe.otc.i18n.appSettingsTailscaleServed
import cloud.offthe.otc.i18n.appSettingsTailscaleSuits
import cloud.offthe.otc.i18n.appSettingsTailscaleTurnOff
import cloud.offthe.otc.i18n.appSettingsTailscaleWorking
import cloud.offthe.otc.i18n.appSettingsThumbsClear
import cloud.offthe.otc.i18n.appSettingsThumbsFooter
import cloud.offthe.otc.i18n.appSettingsThumbsMaxSize
import cloud.offthe.otc.i18n.appSettingsUpdateBack
import cloud.offthe.otc.i18n.appSettingsUpdateBuild
import cloud.offthe.otc.i18n.appSettingsUpdateCheckAgain
import cloud.offthe.otc.i18n.appSettingsUpdateCheckError
import cloud.offthe.otc.i18n.appSettingsUpdateChecking
import cloud.offthe.otc.i18n.appSettingsUpdateCritical
import cloud.offthe.otc.i18n.appSettingsUpdateFailed
import cloud.offthe.otc.i18n.appSettingsUpdateFailedOn
import cloud.offthe.otc.i18n.appSettingsUpdateLabelBuild
import cloud.offthe.otc.i18n.appSettingsUpdateMajor
import cloud.offthe.otc.i18n.appSettingsUpdateRestart
import cloud.offthe.otc.i18n.appSettingsUpdateRestartConfirm
import cloud.offthe.otc.i18n.appSettingsUpdateRestartFailed
import cloud.offthe.otc.i18n.appSettingsUpdateRestartMessage
import cloud.offthe.otc.i18n.appSettingsUpdateRestartNote
import cloud.offthe.otc.i18n.appSettingsUpdateRestartOld
import cloud.offthe.otc.i18n.appSettingsUpdateRestartTitle
import cloud.offthe.otc.i18n.appSettingsUpdateRestarting
import cloud.offthe.otc.i18n.appSettingsUpdateRestartingNote
import cloud.offthe.otc.i18n.appSettingsUpdateSlow
import cloud.offthe.otc.i18n.appSettingsUpdateStartFailed
import cloud.offthe.otc.i18n.appSettingsUpdateStarting
import cloud.offthe.otc.i18n.appSettingsUpdateStartingStatus
import cloud.offthe.otc.i18n.appSettingsUpdateTitle
import cloud.offthe.otc.i18n.appSettingsUpdateUpToDate
import cloud.offthe.otc.i18n.appSettingsUpdateUpdateTo
import cloud.offthe.otc.i18n.appSettingsUpdateUpdateToBuild
import cloud.offthe.otc.i18n.appSettingsUploadsTitle
import cloud.offthe.otc.i18n.appSettingsUsersConfirmDelete
import cloud.offthe.otc.i18n.appSettingsUsersConfirmHint
import cloud.offthe.otc.i18n.appSettingsUsersCreateFailed
import cloud.offthe.otc.i18n.appSettingsUsersCreated
import cloud.offthe.otc.i18n.appSettingsUsersDeleteFailed
import cloud.offthe.otc.i18n.appSettingsUsersDeleted
import cloud.offthe.otc.i18n.appSettingsUsersDisable
import cloud.offthe.otc.i18n.appSettingsUsersEnable
import cloud.offthe.otc.i18n.appSettingsUsersInactive
import cloud.offthe.otc.i18n.appSettingsUsersIntro
import cloud.offthe.otc.i18n.appSettingsUsersLoadUsage
import cloud.offthe.otc.i18n.appSettingsUsersPort
import cloud.offthe.otc.i18n.appSettingsUsersPortDomain
import cloud.offthe.otc.i18n.appSettingsUsersTitle
import cloud.offthe.otc.i18n.appSettingsUsersUpdateFailed
import cloud.offthe.otc.i18n.appSettingsUsersUsage
import cloud.offthe.otc.i18n.appSettingsUsersUsername
import org.junit.Assert.assertEquals
import org.junit.Test
import java.util.Locale

// Settings' text comes from the catalog (app.settings, android.settings) and
// reads exactly as the literals it replaced: every accessor the Settings
// screens use, rendered with sample values (a file name gets the isolation
// marks every user-typed value gets). As SettingsTextTests.swift.
class SettingsTextTest {
    @Test fun everyTextReadsAsBefore() {
        assertEquals("Leave and Delete", S.androidSettingsBridgeLeaveDelete().english())
        assertEquals("Drag the photo to centre your face", S.androidSettingsCropHint().english())
        assertEquals("Zoom", S.androidSettingsCropZoom().english())
        assertEquals("SD card · \u2068DCIM/Camera\u2069", S.androidSettingsFolderOnVolume("SD card", "DCIM/Camera").english())
        assertEquals("Folders", S.androidSettingsFolders().english())
        assertEquals("All", S.androidSettingsFoldersAll().english())
        assertEquals("No photos on this phone yet.", S.androidSettingsFoldersEmpty().english())
        assertEquals("Turning a folder off only stops backing it up: nothing is deleted, from this phone or from your device. Turning it back on backs up everything already in it.", S.androidSettingsFoldersFooter().english())
        assertEquals("Android hides folders whose name starts with a dot, and folders that contain a file called .nomedia, from every app, this one included. Their photos can't be listed here or backed up until the folder is renamed or that file is removed.", S.androidSettingsFoldersHidden().english())
        assertEquals("Every folder is backed up unless you turn it off here. New folders are backed up too, as soon as they appear.", S.androidSettingsFoldersIntro().english())
        assertEquals("Could not list the folders on this phone.", S.androidSettingsFoldersLoadFailed().english())
        assertEquals("1 off", S.androidSettingsFoldersOff(1).english())
        assertEquals("2 off", S.androidSettingsFoldersOff(2).english())
        assertEquals("The shared copies are deleted and the link stops working.", S.androidSettingsLinksDeleteText().english())
        assertEquals("Links made with Share or Share as Gallery in Images and Files show up here until they expire.", S.androidSettingsLinksEmptyText().english())
        assertEquals("Nothing logged yet.", S.androidSettingsLogsEmpty().english())
        assertEquals("What the device and its updates write down as they run - live, to read, share or send to us when something goes wrong.", S.androidSettingsLogsFooter().english())
        assertEquals("Could not load the log", S.androidSettingsLogsLoadFailed().english())
        assertEquals("Could not send the logs", S.androidSettingsLogsSendFailed().english())
        assertEquals("Send logs to us", S.androidSettingsLogsSendTitle().english())
        assertEquals("No photos here yet.", S.androidSettingsPickerEmpty().english())
        assertEquals("Could not load your photos.", S.androidSettingsPickerLoadFailed().english())
        assertEquals("Could not open that photo.", S.androidSettingsPickerOpenFailed().english())
        assertEquals("Description", S.androidSettingsProfileDescription().english())
        assertEquals("From this phone", S.androidSettingsProfileFromPhone().english())
        assertEquals("Galleries and download links you shared, until they expire. Deleting one removes the shared copies and the link stops working.", S.androidSettingsSharingFooter().english())
        assertEquals("Sync from cloud", S.androidSettingsSyncCloud().english())
        assertEquals("Sync", S.androidSettingsSyncTitle().english())
        assertEquals("Change the size", S.androidSettingsThumbsChangeSize().english())
        assertEquals("Maximum size, 4.2 MB", S.androidSettingsThumbsMaxSizeA11y("4.2 MB").english())
        assertEquals("Selected", S.androidSettingsThumbsSelected().english())
        assertEquals("Thumbnail cache", S.androidSettingsThumbsTitle().english())
        assertEquals("Version —", S.androidSettingsUpdateVersionUnknown().english())
        assertEquals("1 pending", S.androidSettingsUploadPending(1).english())
        assertEquals("5 pending", S.androidSettingsUploadPending(5).english())
        assertEquals("Waiting", S.androidSettingsUploadWaiting().english())
        assertEquals("Uploading \u2068IMG_0001.HEIC\u2069", S.androidSettingsUploading("IMG_0001.HEIC").english())
        assertEquals("Your Account Page", S.appSettingsBridgeAccountPage().english())
        assertEquals("At home: 192.168.1.20:8080", S.appSettingsBridgeAtHome("192.168.1.20:8080").english())
        assertEquals("Delete My Account…", S.appSettingsBridgeDeleteAccount().english())
        assertEquals("This device leaves the bridge first and goes on working at home. Then your account page opens: sign in there and delete the account, which releases any other devices' names too.", S.appSettingsBridgeDeleteMessage().english())
        assertEquals("Delete your account?", S.appSettingsBridgeDeleteTitle().english())
        assertEquals("Leaving gives the name back; the device keeps working at home, at http://192.168.1.20:8080.", S.appSettingsBridgeFooter("192.168.1.20:8080").english())
        assertEquals("Leave the Bridge", S.appSettingsBridgeLeave().english())
        assertEquals("Leave the Bridge", S.appSettingsBridgeLeaveConfirm().english())
        assertEquals("Could not leave the bridge.", S.appSettingsBridgeLeaveFailed().english())
        assertEquals("cala.off-the.cloud is given back and the device restarts. It keeps everything and works at home, but is no longer reachable from outside or by your friends. You can join again later.", S.appSettingsBridgeLeaveMessage("cala.off-the.cloud").english())
        assertEquals("Leave the bridge?", S.appSettingsBridgeLeaveTitle().english())
        assertEquals("The device is restarting off the bridge. From now on the app reaches it on your home network, at 192.168.1.20:8080.", S.appSettingsBridgeLeft("192.168.1.20:8080").english())
        assertEquals("Only reachable on your home network. Join the bridge from Settings in the device's web app.", S.appSettingsBridgeLocalOnly().english())
        assertEquals("Reachable from anywhere at cala.off-the.cloud", S.appSettingsBridgeReachable("cala.off-the.cloud").english())
        assertEquals("This device left the bridge: off-the.cloud no longer knew its name (its account was deleted, or the name released). Everything on it is still there.", S.appSettingsBridgeReleased("off-the.cloud").english())
        assertEquals("Bridge and Account", S.appSettingsBridgeTitle().english())
        assertEquals("Device ID: 0123abcd", S.appSettingsConnectionDeviceId("0123abcd").english())
        assertEquals("Log Out", S.appSettingsConnectionLogOut().english())
        assertEquals("Log Out", S.appSettingsConnectionLogOutConfirm().english())
        assertEquals("The connection, the sync history and everything cached from the device are removed from this phone. Nothing on the device itself is deleted.", S.appSettingsConnectionLogOutMessage().english())
        assertEquals("Log out of this device?", S.appSettingsConnectionLogOutTitle().english())
        assertEquals("Save Connection", S.appSettingsConnectionSave().english())
        assertEquals("Connection", S.appSettingsConnectionTitle().english())
        assertEquals("Use photo", S.appSettingsCropUse().english())
        assertEquals("Error updating this setting", S.appSettingsErrorSetting().english())
        assertEquals("Unexpected response", S.appSettingsErrorUnexpected().english())
        assertEquals("Update failed", S.appSettingsErrorUpdateFailed().english())
        assertEquals("Detect faces in newly uploaded photos so you can search by person. Off unless you turn it on (here or during setup), and faces never leave the device. It looks at everyone in your photos, not just you. It only affects photos uploaded while it is on — use Reprocess Media below to scan the ones you already have.", S.appSettingsFacesFooter().english())
        assertEquals("People", S.appSettingsFacesTitle().english())
        assertEquals("Face Recognition", S.appSettingsFacesToggle().english())
        assertEquals("Off The Cloud can't see your photos yet. Allow access to them to choose what is backed up.", S.appSettingsFoldersNoAccess().english())
        assertEquals("1 photo", S.appSettingsFoldersPhotos(1).english())
        assertEquals("2 photos", S.appSettingsFoldersPhotos(2).english())
        assertEquals("1,234 photos · 56 videos", S.appSettingsFoldersPhotosVideos("1,234 photos", "56 videos").english())
        assertEquals("1 video", S.appSettingsFoldersVideos(1).english())
        assertEquals("2 videos", S.appSettingsFoldersVideos(2).english())
        assertEquals("Allow Access to All Photos", S.appSettingsLimitedAllowAll().english())
        assertEquals("Select More Photos", S.appSettingsLimitedSelectMore().english())
        assertEquals("Only the photos you picked are backed up: Off The Cloud can see only those. To back up every photo on this phone, allow access to all of them.", S.appSettingsLimitedWarning().english())
        assertEquals("Created Oct 11, 2026 at 10:30", S.appSettingsLinksCreated("Oct 11, 2026 at 10:30").english())
        assertEquals("Could not delete the link", S.appSettingsLinksDeleteFailed().english())
        assertEquals("Delete link", S.appSettingsLinksDeleteLink().english())
        assertEquals("Delete this link?", S.appSettingsLinksDeleteTitle().english())
        assertEquals("No shared links", S.appSettingsLinksEmpty().english())
        assertEquals("Expires Oct 11, 2026 at 10:30", S.appSettingsLinksExpires("Oct 11, 2026 at 10:30").english())
        assertEquals("Could not load the shared links", S.appSettingsLinksLoadFailed().english())
        assertEquals("Never opened", S.appSettingsLinksNeverOpened().english())
        assertEquals("Opened 1 time", S.appSettingsLinksOpened(1).english())
        assertEquals("Opened 5 times", S.appSettingsLinksOpened(5).english())
        assertEquals("Opened 1 time, last 2 hours ago", S.appSettingsLinksOpenedLast(1, "2 hours ago").english())
        assertEquals("Opened 5 times, last 2 hours ago", S.appSettingsLinksOpenedLast(5, "2 hours ago").english())
        assertEquals("4.2 MB · 1 file", S.appSettingsLinksSizeFiles("4.2 MB", 1).english())
        assertEquals("4.2 MB · 5 files", S.appSettingsLinksSizeFiles("4.2 MB", 5).english())
        assertEquals("Shared Links", S.appSettingsLinksTitle().english())
        assertEquals("Shared gallery", S.appSettingsLinksUntitledGallery().english())
        assertEquals("Shared files (zip)", S.appSettingsLinksUntitledZip().english())
        assertEquals("Device", S.appSettingsLogsDevice().english())
        assertEquals("Note (optional)", S.appSettingsLogsNote().english())
        assertEquals("Sends the last part of your device's logs, your note and your account's email to info@off-the.cloud, so we can help. Logs can include file and folder names, search words, Wi-Fi network names and your device's addresses.", S.appSettingsLogsSendExplain().english())
        assertEquals("Send to us", S.appSettingsLogsSendToUs().english())
        assertEquals("Sending…", S.appSettingsLogsSending().english())
        assertEquals("Sent - we'll answer at your account's email.", S.appSettingsLogsSent().english())
        assertEquals("Logs", S.appSettingsLogsTitle().english())
        assertEquals("Updates", S.appSettingsLogsUpdates().english())
        assertEquals("Logs can include file and folder names, search words, Wi-Fi network names and your device's addresses. Check them before you share them.", S.appSettingsLogsWarning().english())
        assertEquals("Change Password", S.appSettingsPasswordChange().english())
        assertEquals("Password changed ✅", S.appSettingsPasswordChanged().english())
        assertEquals("Changing…", S.appSettingsPasswordChanging().english())
        assertEquals("Confirm new password", S.appSettingsPasswordConfirm().english())
        assertEquals("Current password", S.appSettingsPasswordCurrent().english())
        assertEquals("Error changing password: The device didn't answer in time.", S.appSettingsPasswordError("The device didn't answer in time.").english())
        assertEquals("Change failed", S.appSettingsPasswordFailed().english())
        assertEquals("Fill in all fields", S.appSettingsPasswordFillAll().english())
        assertEquals("Passwords don't match", S.appSettingsPasswordMismatch().english())
        assertEquals("New password", S.appSettingsPasswordNew().english())
        assertEquals("Could not fetch the connection's public key", S.appSettingsPasswordNoKey().english())
        assertEquals("Change Password", S.appSettingsPasswordTitle().english())
        assertEquals("All photos", S.appSettingsPickerAll().english())
        assertEquals("Choose a photo", S.appSettingsPickerTitle().english())
        assertEquals("Change photo", S.appSettingsProfileChangePhoto().english())
        assertEquals("From your device", S.appSettingsProfileFromDevice().english())
        assertEquals("Name", S.appSettingsProfileName().english())
        assertEquals("Save Profile", S.appSettingsProfileSave().english())
        assertEquals("Error saving profile", S.appSettingsProfileSaveError().english())
        assertEquals("Profile update failed", S.appSettingsProfileSaveFailed().english())
        assertEquals("Profile updated ✅", S.appSettingsProfileSaved().english())
        assertEquals("Profile", S.appSettingsProfileTitle().english())
        assertEquals("Reprocess All Media", S.appSettingsReprocessAll().english())
        assertEquals("Last run completed — 1 file(s) processed.", S.appSettingsReprocessCompleted(1).english())
        assertEquals("Last run completed — 5 file(s) processed.", S.appSettingsReprocessCompleted(5).english())
        assertEquals("Reprocess every photo and video in your library? This deletes all existing tags and recognized people and rebuilds them from scratch. It can take a while and can't be undone.", S.appSettingsReprocessConfirmRestart().english())
        assertEquals("Resume reprocessing where it left off? It can take a while.", S.appSettingsReprocessConfirmResume().english())
        assertEquals("Last run failed after 1 file(s) — try again.", S.appSettingsReprocessFailed(1).english())
        assertEquals("Last run failed after 5 file(s) — try again.", S.appSettingsReprocessFailed(5).english())
        assertEquals("Re-run tagging and face detection on every photo and video already in your library — useful after a detection fix or model update. This clears existing tags and recognized people first and rebuilds them from scratch.", S.appSettingsReprocessFooter().english())
        assertEquals("Reprocess", S.appSettingsReprocessRestartButton().english())
        assertEquals("Resume Reprocessing", S.appSettingsReprocessResume().english())
        assertEquals("Resume", S.appSettingsReprocessResumeButton().english())
        assertEquals("Reprocessing… 45% (9 / 20)", S.appSettingsReprocessRunning(45, 9, 20).english())
        assertEquals("Error starting reprocessing", S.appSettingsReprocessStartError().english())
        assertEquals("Could not start reprocessing", S.appSettingsReprocessStartFailed().english())
        assertEquals("Start Over", S.appSettingsReprocessStartOver().english())
        assertEquals("Starting…", S.appSettingsReprocessStarting().english())
        assertEquals("Error stopping reprocessing", S.appSettingsReprocessStopError().english())
        assertEquals("Could not stop reprocessing", S.appSettingsReprocessStopFailed().english())
        assertEquals("Stopped at 45% (9 / 20).", S.appSettingsReprocessStopped(45, 9, 20).english())
        assertEquals("Stopping…", S.appSettingsReprocessStopping().english())
        assertEquals("Reprocess Media", S.appSettingsReprocessTitle().english())
        assertEquals("Sharing", S.appSettingsSharingTitle().english())
        assertEquals("CPU: 12.5% · Mem: 4.6 GB of 984 GB", S.appSettingsStatusCpuMem("12.5", "4.6 GB", "984 GB").english())
        assertEquals("Disk: 4.6 GB of 984 GB", S.appSettingsStatusDisk("4.6 GB", "984 GB").english())
        assertEquals("RAID: In sync · Disks: 2", S.appSettingsStatusRaid("In sync", 2).english())
        assertEquals("Degraded", S.appSettingsStatusRaidDegraded().english())
        assertEquals("In sync", S.appSettingsStatusRaidInSync().english())
        assertEquals("RAID: raid1 — In sync · Disks: 2", S.appSettingsStatusRaidLevel("raid1", "In sync", 2).english())
        assertEquals("No RAID", S.appSettingsStatusRaidNone().english())
        assertEquals("Syncing (45%)", S.appSettingsStatusRaidSyncing(45).english())
        assertEquals("Unknown", S.appSettingsStatusRaidUnknown().english())
        assertEquals("RAID used: 4.6 GB of 984 GB", S.appSettingsStatusRaidUsed("4.6 GB", "984 GB").english())
        assertEquals("Status", S.appSettingsStatusTitle().english())
        assertEquals("45%", S.appSettingsStatusUsedPercent(45).english())
        assertEquals("Sync All", S.appSettingsSyncAll().english())
        assertEquals("Authorize Photos Access", S.appSettingsSyncAuthorize().english())
        assertEquals("Sync All goes through the whole library again. Sync From Now skips everything already in it: only photos and videos taken from now on are uploaded.", S.appSettingsSyncFooter().english())
        assertEquals("Sync From Now", S.appSettingsSyncFromNow().english())
        assertEquals("Sync Options", S.appSettingsSyncTitle().english())
        assertEquals("Include videos", S.appSettingsSyncVideos().english())
        assertEquals("Wi-Fi only", S.appSettingsSyncWifiOnly().english())
        assertEquals("Recognise what newly uploaded photos and videos show (a beach, a dog, a birthday cake) so you can search for it. It runs on this device and nothing leaves it. Turning it off saves processing time; places from a photo's own location data are still searchable. It only affects what is uploaded while it is off.", S.appSettingsTaggingFooter().english())
        assertEquals("Image Tagging", S.appSettingsTaggingTitle().english())
        assertEquals("Auth key (optional)", S.appSettingsTailscaleAuthKey().english())
        assertEquals("Authorise this device in the page that just opened, then press Enable again.", S.appSettingsTailscaleAuthorise().english())
        assertEquals("Could not change the setting.", S.appSettingsTailscaleChangeFailed().english())
        assertEquals("Done — this device is reachable at https://cala.tail1234.ts.net", S.appSettingsTailscaleDone("https://cala.tail1234.ts.net").english())
        assertEquals("Enable Tailscale Funnel", S.appSettingsTailscaleEnable().english())
        assertEquals("Tailscale Funnel is an alternative to the Off The Cloud bridge, it publishes this device on your own ts.net address but with the next limitations:", S.appSettingsTailscaleIntro().english())
        assertEquals("With a key from your Tailscale admin console this finishes on its own. Without one, you'll be taken to Tailscale to authorise this device.", S.appSettingsTailscaleKeyHint().english())
        assertEquals("You'll need a Tailscale account, with Funnel enabled for your tailnet.", S.appSettingsTailscaleLimitAccount().englishText())
        assertEquals("Bandwidth is capped. Tailscale limits what can pass through Funnel and doesn't publish the limit.", S.appSettingsTailscaleLimitBandwidth().englishText())
        assertEquals("One user only. Extra users need a web address each, and Funnel gives this machine a single one.", S.appSettingsTailscaleLimitOneUser().englishText())
        assertEquals("The social side won't work. Friends find each other through the bridge, so sharing and friends' timelines are unavailable.", S.appSettingsTailscaleLimitSocial().englishText())
        assertEquals("Tailscale is connected, but Funnel didn't come up. Check that Funnel is enabled for your tailnet.", S.appSettingsTailscaleNoFunnel().english())
        assertEquals("Tailscale isn't installed on this device, so Funnel isn't available here.", S.appSettingsTailscaleNotInstalled().english())
        assertEquals("Tailscale isn't installed on this device, so Funnel can't be turned on here.", S.appSettingsTailscaleNotInstalledOn().english())
        assertEquals("Funnel is off. This device is reachable through the bridge again.", S.appSettingsTailscaleOff().english())
        assertEquals("Served over Tailscale Funnel at https://cala.tail1234.ts.net", S.appSettingsTailscaleServed("https://cala.tail1234.ts.net").english())
        assertEquals("It suits a device you want purely as your own private NAS — your files and photos, reachable from anywhere, nothing shared with anyone.", S.appSettingsTailscaleSuits().english())
        assertEquals("Turn Funnel off", S.appSettingsTailscaleTurnOff().english())
        assertEquals("Working…", S.appSettingsTailscaleWorking().english())
        assertEquals("Clear thumbnail cache", S.appSettingsThumbsClear().english())
        assertEquals("Thumbnails are kept on this phone so Images opens without downloading them again.", S.appSettingsThumbsFooter().english())
        assertEquals("Maximum size", S.appSettingsThumbsMaxSize().english())
        assertEquals("The device is back.", S.appSettingsUpdateBack().english())
        assertEquals("build 118", S.appSettingsUpdateBuild("118").english())
        assertEquals("Check again", S.appSettingsUpdateCheckAgain().english())
        assertEquals("Couldn't reach the update server just now, so this may be out of date.", S.appSettingsUpdateCheckError().english())
        assertEquals("Checking…", S.appSettingsUpdateChecking().english())
        assertEquals("Critical", S.appSettingsUpdateCritical().english())
        assertEquals("Update failed: Building the new version", S.appSettingsUpdateFailed("Building the new version").english())
        assertEquals("Update failed on Oct 11, 2026 at 10:30: Building the new version", S.appSettingsUpdateFailedOn("Oct 11, 2026 at 10:30", "Building the new version").english())
        assertEquals("2.3 (build 118)", S.appSettingsUpdateLabelBuild("2.3", "118").english())
        assertEquals("Major", S.appSettingsUpdateMajor().english())
        assertEquals("Restart Device", S.appSettingsUpdateRestart().english())
        assertEquals("Restart", S.appSettingsUpdateRestartConfirm().english())
        assertEquals("Could not restart the device.", S.appSettingsUpdateRestartFailed().english())
        assertEquals("It will be unreachable for about a minute.", S.appSettingsUpdateRestartMessage().english())
        assertEquals("The device rebuilds itself and restarts, which takes a few minutes and drops this connection on the way. Your photos and settings are left alone.", S.appSettingsUpdateRestartNote().english())
        assertEquals("Your device needs an update before it can be restarted from here.", S.appSettingsUpdateRestartOld().english())
        assertEquals("Restart the device?", S.appSettingsUpdateRestartTitle().english())
        assertEquals("Restarting…", S.appSettingsUpdateRestarting().english())
        assertEquals("Restarting… the device will be back in about a minute.", S.appSettingsUpdateRestartingNote().english())
        assertEquals("The device hasn't come back yet. It can take a little longer; if it doesn't come back, unplug it and plug it back in.", S.appSettingsUpdateSlow().english())
        assertEquals("Could not start the update.", S.appSettingsUpdateStartFailed().english())
        assertEquals("Starting…", S.appSettingsUpdateStarting().english())
        assertEquals("Starting", S.appSettingsUpdateStartingStatus().english())
        assertEquals("Device version", S.appSettingsUpdateTitle().english())
        assertEquals("Up to date", S.appSettingsUpdateUpToDate().english())
        assertEquals("Update to 2.4", S.appSettingsUpdateUpdateTo("2.4").english())
        assertEquals("Update to build 118", S.appSettingsUpdateUpdateToBuild("118").english())
        assertEquals("Uploads", S.appSettingsUploadsTitle().english())
        assertEquals("Confirm Delete", S.appSettingsUsersConfirmDelete().english())
        assertEquals("Type \"ana\" to confirm deletion.", S.appSettingsUsersConfirmHint("ana").english())
        assertEquals("Could not create user.", S.appSettingsUsersCreateFailed().english())
        assertEquals("User \"ana\" created.", S.appSettingsUsersCreated("ana").english())
        assertEquals("Could not delete user.", S.appSettingsUsersDeleteFailed().english())
        assertEquals("User \"ana\" deleted.", S.appSettingsUsersDeleted("ana").english())
        assertEquals("Disable", S.appSettingsUsersDisable().english())
        assertEquals("Enable", S.appSettingsUsersEnable().english())
        assertEquals("INACTIVE", S.appSettingsUsersInactive().english())
        assertEquals("Each user runs as its own fully separate instance — own port, own database, own storage — managed only from here.", S.appSettingsUsersIntro().english())
        assertEquals("Load usage", S.appSettingsUsersLoadUsage().english())
        assertEquals("port", S.appSettingsUsersPort().english())
        assertEquals("port 8081 · cala.off-the.cloud", S.appSettingsUsersPortDomain("8081", "cala.off-the.cloud").english())
        assertEquals("Users", S.appSettingsUsersTitle().english())
        assertEquals("Could not update this user.", S.appSettingsUsersUpdateFailed().english())
        assertEquals("312 MB (12.5%) · 1 active", S.appSettingsUsersUsage("312", "12.5", 1).english())
        assertEquals("312 MB (12.5%) · 5 active", S.appSettingsUsersUsage("312", "12.5", 5).english())
        assertEquals("username", S.appSettingsUsersUsername().english())
    }

    @Test fun textsBuiltFromPartsReadAsBefore() {
        var st = UpdateViewModel.State(currentVersion = 45, latestVersion = 46)
        assertEquals("build 45", st.installedText.english())
        assertEquals("build 46", st.latestText.english())
        assertEquals("Update to build 46", st.updateToText.english())
        st = st.copy(currentLabel = "1.1", latestLabel = "1.2", message = "Building")
        assertEquals("1.1 (build 45)", st.installedText.english())
        assertEquals("1.2", st.latestText.english())
        assertEquals("Update to 1.2", st.updateToText.english())
        assertEquals("Building", st.statusMessage.english())
        assertEquals("Starting", st.copy(startedHere = true).statusMessage.english())
        assertEquals("ours", deviceOr("", UiText.Raw("ours")).english())
        assertEquals("the device's", deviceOr("the device's", UiText.Raw("ours")).english())
    }

    /** A rich text in English as RichText.resolve builds it, its spans plain (as Settings shows them). */
    private fun RichText.englishText(): String {
        val template = when (this) {
            is RichText.Res -> EnglishStrings.string(id).first
            is RichText.Plural -> EnglishStrings.plural(id, count).first
        }
        return UiTextFormat.rich(template, emptyList(), Locale.US, emptyMap()).text
    }
}
