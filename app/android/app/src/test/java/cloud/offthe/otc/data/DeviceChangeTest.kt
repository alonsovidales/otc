// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

// Another address saved in Settings is a change of device until its device
// says otherwise (DeviceChange, as iOS's DeviceChangeTests): the rules and
// the check's store, with the verdict's actions watched instead of done
// (they need the app: PhotoSync, SyncChoice, the wallpaper).
class DeviceChangeTest {
    private class Memory : DeviceChange.Store {
        var value: String? = null
        override fun get() = value
        override fun set(value: String?) { this.value = value }
    }

    private val memory = Memory()
    private val verdicts = mutableListOf<DeviceChange.Verdict>()
    private val pinA = ByteArray(32) { 1 }
    private val pinB = ByteArray(32) { 2 }

    @Before fun setUp() {
        DeviceChange.store = memory
        DeviceChange.onVerdict = { verdicts += it }
    }

    @After fun tearDown() {
        memory.value = null
    }

    @Test fun theSameDeviceOnlyWhenBothPinsAreKnownAndEqual() {
        assertEquals(DeviceChange.Verdict.SAME_DEVICE, DeviceChange.verdict(pinA, pinA.copyOf()))
        assertEquals(DeviceChange.Verdict.ANOTHER_DEVICE, DeviceChange.verdict(pinA, pinB))
        // A device that can't say, or a phone that never learnt the last one's.
        assertEquals(DeviceChange.Verdict.ANOTHER_DEVICE, DeviceChange.verdict(pinA, null))
        assertEquals(DeviceChange.Verdict.ANOTHER_DEVICE, DeviceChange.verdict(ByteArray(0), null))
        assertEquals(DeviceChange.Verdict.ANOTHER_DEVICE, DeviceChange.verdict(ByteArray(0), ByteArray(0)))
    }

    @Test fun onlyAnotherAddressIsAChange() {
        assertTrue(DeviceChange.isChange("wss://a.off-the.cloud/ws", "wss://b.off-the.cloud/ws", sameDevice = false))
        // A first sign-in, the address cleared, the same one spelled otherwise.
        assertFalse(DeviceChange.isChange("", "wss://b.off-the.cloud/ws", sameDevice = false))
        assertFalse(DeviceChange.isChange("wss://a.off-the.cloud/ws", "", sameDevice = false))
        assertFalse(DeviceChange.isChange("wss://a.off-the.cloud/ws", "wss://A.off-the.cloud/ws", sameDevice = false))
        // Leave the Bridge: the same device at its home address.
        assertFalse(DeviceChange.isChange("wss://a.off-the.cloud/ws", "ws://192.168.1.20:8080/ws", sameDevice = true))
    }

    @Test fun theSamePinAsksNothing() {
        DeviceChange.begin(pinA)
        assertTrue(DeviceChange.pending)
        DeviceChange.resolve(pinA.copyOf())
        assertEquals(listOf(DeviceChange.Verdict.SAME_DEVICE), verdicts)
        assertFalse(DeviceChange.pending)
    }

    @Test fun anotherPinOrNoneIsAnotherDevice() {
        DeviceChange.begin(pinA)
        DeviceChange.resolve(pinB)
        DeviceChange.begin(pinA)
        DeviceChange.resolve(null)
        // No pin known for the last device: whatever the new one says.
        DeviceChange.begin(null)
        DeviceChange.resolve(pinA)
        assertEquals(List(3) { DeviceChange.Verdict.ANOTHER_DEVICE }, verdicts)
        assertFalse(DeviceChange.pending)
    }

    @Test fun aCheckWaitingKeepsTheFirstPin() {
        // A, then an address that never answered, then A again: no change.
        DeviceChange.begin(pinA)
        DeviceChange.begin(pinB)
        DeviceChange.resolve(pinA)
        assertEquals(listOf(DeviceChange.Verdict.SAME_DEVICE), verdicts)
    }

    @Test fun nothingWaitingNothingDone() {
        assertFalse(DeviceChange.pending)
        DeviceChange.resolve(pinA)
        DeviceChange.resolve(null)
        assertTrue(verdicts.isEmpty())
        assertNull(DeviceChange.take(pinA))
    }
}
