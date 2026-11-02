// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.appSettingsTaggingFooter
import cloud.offthe.otc.i18n.appSettingsTaggingHeavy
import cloud.offthe.otc.i18n.english
import cloud.offthe.otc.proto.RespUsers
import cloud.offthe.otc.proto.Settings
import cloud.offthe.otc.proto.User
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// What a device's memory allows (release 160; release 164, issue #209): no
// Image Tagging under 3 GB (Settings.image_tagging_available false), a note
// on what it costs under 6 GB (image_tagging_heavy), no other users under 3 GB
// (RespUsers.can_add false). A device before it says none of it: all as before.
class MemoryTiersTest {
    private fun settings(enabled: Boolean, available: Boolean? = null, heavy: Boolean? = null): Settings =
        Settings.newBuilder().setImageTaggingEnabled(enabled)
            .apply { available?.let { setImageTaggingAvailable(it) } }
            .apply { heavy?.let { setImageTaggingHeavy(it) } }
            .build()

    private fun tagging(s: Settings) = DeviceSettingsViewModel.State().withSettings(s)

    @Test fun taggingAsBeforeOnADeviceThatDoesntSay() {
        assertTrue(DeviceSettingsViewModel.State().imageTaggingAvailable)
        val on = tagging(settings(enabled = true))
        assertTrue(on.imageTaggingAvailable)
        assertTrue(on.imageTaggingEnabled)
        val off = tagging(settings(enabled = false))
        assertTrue(off.imageTaggingAvailable)
        assertFalse(off.imageTaggingEnabled)
    }

    @Test fun taggingAvailableShowsTheSwitchAsTheDeviceSays() {
        val on = tagging(settings(enabled = true, available = true))
        assertTrue(on.imageTaggingAvailable)
        assertTrue(on.imageTaggingEnabled)
        val off = tagging(settings(enabled = false, available = true))
        assertTrue(off.imageTaggingAvailable)
        assertFalse(off.imageTaggingEnabled)
    }

    @Test fun taggingUnavailableIsOffWhateverTheSetting() {
        val s = tagging(settings(enabled = false, available = false))
        assertFalse(s.imageTaggingAvailable)
        assertFalse(s.imageTaggingEnabled)
        // The device says off there; off even if it said otherwise.
        val odd = tagging(settings(enabled = true, available = false))
        assertFalse(odd.imageTaggingAvailable)
        assertFalse(odd.imageTaggingEnabled)
        // A later answer from a device that has it again (more memory) shows it again.
        val back = odd.withSettings(settings(enabled = true, available = true))
        assertTrue(back.imageTaggingAvailable)
        assertTrue(back.imageTaggingEnabled)
    }

    @Test fun theTaggingNoteShowsOnlyWhereTheDeviceSaysItIsHeavy() {
        // An older device, and one with 6 GB or more, say nothing: no note.
        assertFalse(DeviceSettingsViewModel.State().imageTaggingHeavy)
        assertFalse(tagging(settings(enabled = true)).imageTaggingHeavy)
        assertFalse(tagging(settings(enabled = true, available = true)).imageTaggingHeavy)
        assertFalse(tagging(settings(enabled = false, available = true, heavy = false)).imageTaggingHeavy)
        // Release 164 on 4 GB (issue #209): off until turned on, with the
        // note - whether the switch is on or off.
        val off = tagging(settings(enabled = false, available = true, heavy = true))
        assertTrue(off.imageTaggingHeavy)
        assertTrue(off.imageTaggingAvailable)
        assertFalse(off.imageTaggingEnabled)
        val on = tagging(settings(enabled = true, available = true, heavy = true))
        assertTrue(on.imageTaggingHeavy)
        assertTrue(on.imageTaggingEnabled)
        // A device that doesn't say whether it has it, has it.
        assertTrue(tagging(settings(enabled = false, heavy = true)).imageTaggingHeavy)
        // Each answer says it again.
        assertFalse(on.withSettings(settings(enabled = true, available = true)).imageTaggingHeavy)
    }

    @Test fun noTaggingNoteWhereThereIsNoTagging() {
        // Under 3 GB the switch is off and disabled and the footer says why:
        // the note about its cost doesn't show.
        val s = tagging(settings(enabled = false, available = false, heavy = true))
        assertFalse(s.imageTaggingAvailable)
        assertFalse(s.imageTaggingHeavy)
    }

    @Test fun theNotesReadAsTheCatalogSays() {
        assertTrue(S.appSettingsTaggingFooter().english().endsWith("It needs a device with at least 4 GB of memory."))
        assertEquals("On this device Image Tagging uses about 1–2 GB more memory, and new photos take longer to process.", S.appSettingsTaggingHeavy().english())
    }

    @Test fun theOtherSwitchesComeAsBefore() {
        val s = DeviceSettingsViewModel.State().withSettings(
            Settings.newBuilder().setFaceRecognitionEnabled(true).setImageTaggingAvailable(false).setPlacesEnabled(false).setMemoriesEnabled(true).build()
        )
        assertTrue(s.faceRecognitionEnabled)
        assertEquals(false, s.placesEnabled)
        assertEquals(true, s.memoriesEnabled)
        // A device before Smart search: no switch.
        assertNull(s.smartSearchEnabled)
    }

    private fun users(canAdd: Boolean? = null): RespUsers =
        RespUsers.newBuilder()
            .addUsers(User.newBuilder().setUuid("u1").setUsername("ana").setActive(true))
            .apply { canAdd?.let { setCanAdd(it) } }
            .build()

    @Test fun addingUsersAsBeforeOnADeviceThatDoesntSay() {
        assertTrue(UsersManagementViewModel.State().canAdd)
        val s = UsersManagementViewModel.State().withUsers(users())
        assertTrue(s.canAdd)
        assertEquals(listOf("ana"), s.users.map { it.username })
    }

    @Test fun addingUsersWhereTheDeviceTakesThem() {
        assertTrue(UsersManagementViewModel.State().withUsers(users(canAdd = true)).canAdd)
    }

    @Test fun noAddingUsersWhereTheDeviceTakesNoMoreButTheUsersStay() {
        val s = UsersManagementViewModel.State().withUsers(users(canAdd = false))
        assertFalse(s.canAdd)
        assertEquals(listOf("ana"), s.users.map { it.username })
        // Each answer says it again.
        assertTrue(s.withUsers(users(canAdd = true)).canAdd)
        assertTrue(s.withUsers(users()).canAdd)
    }
}
