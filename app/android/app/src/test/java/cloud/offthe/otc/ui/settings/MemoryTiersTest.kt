// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.settings

import cloud.offthe.otc.proto.RespUsers
import cloud.offthe.otc.proto.Settings
import cloud.offthe.otc.proto.User
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// What a device's memory allows (release 160): no Image Tagging under 6 GB
// (Settings.image_tagging_available false), no other users under 3 GB
// (RespUsers.can_add false). A device before it says neither: both as before.
class MemoryTiersTest {
    private fun settings(enabled: Boolean, available: Boolean? = null): Settings =
        Settings.newBuilder().setImageTaggingEnabled(enabled).apply { available?.let { setImageTaggingAvailable(it) } }.build()

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
