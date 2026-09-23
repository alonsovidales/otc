// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import java.security.KeyFactory
import java.security.spec.MGF1ParameterSpec
import java.security.spec.X509EncodedKeySpec
import javax.crypto.Cipher
import javax.crypto.spec.OAEPParameterSpec
import javax.crypto.spec.PSource

// Port of PwCrypto.swift (issue #2): the device hands out an ephemeral RSA
// public key per connection and the password is encrypted with it
// (RSA-OAEP/SHA-256) before it ever leaves the app, so the bridge only
// ever relays ciphertext. The device sends X.509 SubjectPublicKeyInfo -
// exactly what X509EncodedKeySpec takes, so no DER unwrapping is needed
// here, unlike on iOS.
object PwCrypto {
    fun encryptPassword(password: String, pubKeyDER: ByteArray): ByteArray {
        val key = KeyFactory.getInstance("RSA").generatePublic(X509EncodedKeySpec(pubKeyDER))
        val cipher = Cipher.getInstance("RSA/ECB/OAEPWithSHA-256AndMGF1Padding")
        // Explicit: on some providers the MGF1 digest defaults to SHA-1
        // even when the name says SHA-256, and Go's rsa.EncryptOAEP with
        // sha256 needs SHA-256 for both.
        val params = OAEPParameterSpec("SHA-256", "MGF1", MGF1ParameterSpec.SHA256, PSource.PSpecified.DEFAULT)
        cipher.init(Cipher.ENCRYPT_MODE, key, params)
        return cipher.doFinal(password.toByteArray(Charsets.UTF_8))
    }
}
