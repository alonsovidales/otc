// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appShellAccountDisabled
import cloud.offthe.otc.i18n.appShellAuthFailed
import cloud.offthe.otc.i18n.appShellDeviceUnavailable
import cloud.offthe.otc.i18n.appShellNoPublicKey
import cloud.offthe.otc.proto.RespEnvelope

// The public demo device (docs/demo-device.md): anyone may sign in to it
// with the password it publishes, to look around before having a device of
// their own, and it refuses every change (error_code "demo", ErrorCodes).
// It says so before sign-in (PubKey.demo, PubKey.demo_password) and after
// (Status.demo); the app keeps the answer as OTCConnection.isDemo.
object DemoDevice {
    /** Its name on the bridge: "Try the demo" dials it as the forms dial any device of that name. */
    const val NAME = "demo"

    /** wss://demo.off-the.cloud/ws. */
    val endpoint: String get() = SecretsStore.bridgeEndpoint(NAME)

    /** What "Try the demo" makes of GetPubKey's answer, asked before signing in. */
    sealed interface Answer {
        /** A demo device: sign in with [password], as one typed in the form would. */
        class SignIn(val password: String) : Answer
        /** Not one, or no answer from it: [text] says why, in the connection card's words. */
        class Refused(val text: UiText) : Answer
    }

    fun answer(resp: RespEnvelope): Answer {
        if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_PUB_KEY) {
            // The bridge's own verdict when the device isn't there; the
            // device's words otherwise, as the sign-in shows them.
            val ack = if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK) resp.respAck else null
            return Answer.Refused(
                when (ack?.code) {
                    "device_unreachable" -> S.appShellDeviceUnavailable()
                    "account_disabled" -> S.appShellAccountDisabled()
                    else -> ack?.errorMsg?.ifEmpty { null }?.let { UiText.Raw(it) } ?: S.appShellNoPublicKey()
                },
            )
        }
        val key = resp.respPubKey
        // Any other device: no password to sign in with.
        if (!key.demo || key.demoPassword.isEmpty()) return Answer.Refused(S.appShellAuthFailed())
        return Answer.SignIn(key.demoPassword)
    }
}
