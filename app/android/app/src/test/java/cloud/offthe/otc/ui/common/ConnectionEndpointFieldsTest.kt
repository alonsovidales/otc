// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.ui.common

import androidx.compose.runtime.saveable.SaverScope
import cloud.offthe.otc.data.SecretsStore
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

// The connection forms' address (ConnectionEndpointFields.kt): the device
// name shown and the address Save sends are one holder's, so a recreation
// of the Activity (a change of language from the screen's menu) or the
// process dying can keep both or neither, never one alone - a saveable
// name once outlived an address held elsewhere, and Save sent a device the
// field didn't show.
class ConnectionEndpointFieldsTest {
    private val cala = "wss://cala.off-the.cloud/ws"
    private val pit = "wss://pit.off-the.cloud/ws"
    private val lan = "ws://192.168.1.20:8080/ws"

    /** What the field shows is what Save sends. */
    private fun assertShowsWhatItSends(e: EndpointEntry) {
        if (e.customAddress) return // the field is the address itself
        val name = e.deviceName.trim()
        assertEquals(if (name.isEmpty()) "" else SecretsStore.bridgeEndpoint(name), e.endpoint)
    }

    /** Through rememberSaveable's saved state and back, as a recreation does. */
    private fun recreated(e: EndpointEntry): EndpointEntry {
        val saved = with(EndpointEntry.Saver) { SaverScope { true }.save(e) }!!
        return EndpointEntry.Saver.restore(saved)!!
    }

    @Test fun aBridgeAddressOpensAsItsNameAndAnyOtherAsTheAddress() {
        EndpointEntry(cala).let {
            assertEquals("cala", it.deviceName)
            assertFalse(it.customAddress)
            assertEquals(cala, it.endpoint)
        }
        EndpointEntry(lan).let {
            assertTrue(it.customAddress)
            assertEquals(lan, it.endpoint)
        }
        EndpointEntry("").let {
            assertFalse(it.customAddress)
            assertEquals("", it.deviceName)
            assertEquals("", it.endpoint)
        }
    }

    @Test fun theNameIsTheAddressAndTheToggleKeepsWhatItCan() {
        val e = EndpointEntry("")
        e.typeName("Pit ")
        assertEquals(pit, e.endpoint)
        e.typeName("  ")
        assertEquals("", e.endpoint)
        e.typeName("pit")
        // To custom: the address stays.
        e.useCustomAddress(true)
        assertEquals(pit, e.endpoint)
        // Back to the name: the address's name, or nothing for another address.
        e.typeAddress(lan)
        e.useCustomAddress(false)
        assertEquals("", e.deviceName)
        assertEquals("", e.endpoint)
        e.useCustomAddress(true)
        e.typeAddress(cala)
        e.useCustomAddress(false)
        assertEquals("cala", e.deviceName)
        assertEquals(cala, e.endpoint)
    }

    // Settings: the address and its name are saveable together.
    @Test fun aRecreationKeepsTheNameWithItsAddress() {
        val typed = EndpointEntry(cala).apply { typeName("pit") }
        recreated(typed).let {
            assertEquals("pit", it.deviceName)
            assertEquals(pit, it.endpoint)
            assertFalse(it.customAddress)
            assertShowsWhatItSends(it)
        }
        val custom = EndpointEntry(cala).apply { useCustomAddress(true); typeAddress(lan) }
        recreated(custom).let {
            assertTrue(it.customAddress)
            assertEquals(lan, it.endpoint)
        }
    }

    // Welcome and the "can't connect" card: the view model holds both.
    @Test fun theFormsModelKeepsTheNameWithItsAddressOrLosesBoth() {
        val model = ConnectionFormModel()
        val form = ConnectionForm(model, cala, "secret")
        form.entry.typeName("pit")
        form.password = "typed"
        // A change of language: the Activity and its form again, the same model.
        ConnectionForm(model, cala, "secret").let {
            assertEquals("pit", it.entry.deviceName)
            assertEquals(pit, it.endpoint)
            assertEquals("typed", it.password)
            assertShowsWhatItSends(it.entry)
        }
        // The process gone: a new model, so the screen's starting address
        // and its name, never the name typed before.
        ConnectionForm(ConnectionFormModel(), cala, "secret").let {
            assertEquals("cala", it.entry.deviceName)
            assertEquals(cala, it.endpoint)
            assertEquals("secret", it.password)
            assertShowsWhatItSends(it.entry)
        }
    }
}
