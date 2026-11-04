// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import com.google.protobuf.InvalidProtocolBufferException
import org.junit.Assert.assertFalse
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
}
