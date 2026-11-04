// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import com.google.protobuf.InvalidProtocolBufferException
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.security.GeneralSecurityException
import java.security.KeyStoreException
import java.security.ProviderException
import java.security.UnrecoverableKeyException
import javax.crypto.AEADBadTagException

// The secure store is started over only when its keyset can never be read
// again - a file restored or moved from another phone without its Keystore
// key, or a corrupt one - never for a Keystore error that passes (busy, or
// not ready after an OS update): that wiped the sign-in, the device id and
// the sync's watermark after two quick tries.
class SecretsRecoveryTest {
    @Test fun aKeysetThatCanNeverBeReadIsStartedOver() {
        assertTrue(SecretsStore.keysetUnusable(AEADBadTagException("decrypt")))
        assertTrue(SecretsStore.keysetUnusable(GeneralSecurityException("keyset", AEADBadTagException("tag"))))
        assertTrue(SecretsStore.keysetUnusable(UnrecoverableKeyException("no key")))
        assertTrue(SecretsStore.keysetUnusable(RuntimeException(InvalidProtocolBufferException("corrupt"))))
    }

    @Test fun aKeystoreErrorThatPassesKeepsTheFile() {
        assertFalse(SecretsStore.keysetUnusable(KeyStoreException("busy")))
        assertFalse(SecretsStore.keysetUnusable(ProviderException("Keystore key generation failed")))
        assertFalse(SecretsStore.keysetUnusable(GeneralSecurityException("keystore", IllegalStateException("not ready"))))
        assertFalse(SecretsStore.keysetUnusable(IllegalStateException()))
    }

    private class Memory : SecretsFailures.Store {
        var record: SecretsFailures.Record? = null
        override fun read() = record
        override fun write(r: SecretsFailures.Record?) { record = r }
    }

    private val hour = 60L * 60 * 1000

    /**
     * An error that may pass but never does (a key whose blob the Keystore
     * no longer takes after an update) starts over once it has failed on 3
     * launches over a day: it used to hold the owner at "can't open" for good.
     */
    @Test fun anErrorThatNeverPassesStartsOverAfterADayOfLaunches() {
        val memory = Memory()
        val t0 = 1_800_000_000_000L
        // The first launch, tried again and again (Try again): one launch.
        val first = SecretsFailures(memory, "launch 1")
        assertFalse(first.failed(t0))
        assertFalse(first.failed(t0 + 30 * hour))
        assertEquals(1, memory.record?.launches)
        // Launches within the day keep the file, however many.
        assertFalse(SecretsFailures(memory, "launch 2").failed(t0 + hour))
        assertFalse(SecretsFailures(memory, "launch 3").failed(t0 + 2 * hour))
        assertFalse(SecretsFailures(memory, "launch 4").failed(t0 + 23 * hour))
        // A day on, on a launch of its own.
        assertTrue(SecretsFailures(memory, "launch 5").failed(t0 + 25 * hour))
    }

    @Test fun aDayWithFewerLaunchesKeepsTheFile() {
        val memory = Memory()
        val t0 = 1_800_000_000_000L
        assertFalse(SecretsFailures(memory, "a").failed(t0))
        assertFalse(SecretsFailures(memory, "b").failed(t0 + 48 * hour))
        assertTrue(SecretsFailures(memory, "c").failed(t0 + 49 * hour))
    }

    /** An error that passed is forgotten: the next one starts counting again. */
    @Test fun anOpenThatWorksForgetsTheFailures() {
        val memory = Memory()
        val t0 = 1_800_000_000_000L
        SecretsFailures(memory, "a").failed(t0)
        SecretsFailures(memory, "b").failed(t0 + hour)
        SecretsFailures(memory, "c").opened()
        assertNull(memory.record)
        assertFalse(SecretsFailures(memory, "d").failed(t0 + 30 * hour))
        assertFalse(SecretsFailures(memory, "e").failed(t0 + 31 * hour))
        assertEquals(t0 + 30 * hour, memory.record?.firstAt)
    }

    /** A first failure ahead of the clock (set back since) counts from now. */
    @Test fun aClockSetBackStartsTheDayAgain() {
        val memory = Memory()
        val t0 = 1_800_000_000_000L
        SecretsFailures(memory, "a").failed(t0)
        SecretsFailures(memory, "b").failed(t0 + hour)
        assertFalse(SecretsFailures(memory, "c").failed(t0 - 100 * hour))
        assertEquals(SecretsFailures.Record(t0 - 100 * hour, 1, "c"), memory.record)
    }
}
