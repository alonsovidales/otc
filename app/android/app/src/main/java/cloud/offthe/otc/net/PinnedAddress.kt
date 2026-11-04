// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import cloud.offthe.otc.data.SecretsStore
import cloud.offthe.otc.proto.GetPubKey
import kotlinx.coroutines.CancellationException
import java.net.URI

/**
 * A ws:// device address (a device set up without the bridge, or that left
 * it) whose device this phone has signed in to over its pinned home link
 * (HomeEndpoint.reached): it goes only that way from then on, as iOS's
 * OTCConnection.pinnedOnly. A private address or a .local name is the home
 * network only while the phone is at home; on another network using the
 * same range, whoever held that address got the sign-in in the clear - the
 * key the password is sealed with included (CleartextRule judges an
 * address, not the network it is on).
 *
 * Until it is reached - the first sign-in, a device without the home
 * listener, one whose encrypted port this phone can't reach - ws:// goes as
 * before: the pinned link first, then the address itself, on the home
 * network only (CleartextRule). The rules here are pure; OTCConnection makes
 * the moves (PinnedAddressTest runs them against stand-in devices).
 */
object PinnedAddress {
    /** How long whatever answers a pinned ws:// address is given to answer its GetPubKey ([probe]): there is no bridge in between. */
    const val PROBE_TIMEOUT_MS = 15_000L

    /** Whether [url] is a ws:// address: no encryption on the way. */
    fun isCleartext(url: String): Boolean = url.trim().lowercase().startsWith("ws://")

    /**
     * Whether [url] goes only the pinned way: ws://, its device's
     * certificate known ([stored], learnt at a sign-in there or carried over
     * from the bridge by Leave the Bridge) and its pinned link reached from
     * this phone once.
     */
    fun pinnedOnly(url: String, stored: HomeEndpoint?): Boolean = isCleartext(url) && stored?.reached == true

    /**
     * What a connect to [url] races: what the device listed, and for a ws://
     * address its own host too, at the pinned port (HomeEndpoint.including).
     */
    fun raceTargets(url: String, stored: HomeEndpoint): HomeEndpoint =
        if (isCleartext(url)) stored.including(host(url)) else stored

    /**
     * A pinned address's connect failed with [e]: whether it is tried again
     * with the backoff, as the configured endpoint's is (its socket is the
     * current one before it dials, so a failed connect comes back through
     * handleDisconnect). The pinned way installs nothing until signed in,
     * so a device restarting for an update was left until a request, a
     * wake or the card's Save & Retry. Not when [passwordRefused] (the
     * device's verdict: its own gate and backoff), the device can't reach
     * its storage ([storageHeld]: asked again every 30 s), or the attempt
     * was dropped (new settings, Log Out).
     */
    fun retriesAfter(e: Throwable, passwordRefused: Boolean, storageHeld: Boolean): Boolean =
        e !is CancellationException && !passwordRefused && !storageHeld

    /**
     * What saving [after] over [before] leaves of the home record [home]
     * (the endpoint it was learnt on, and what was learnt): the same device
     * at its new address ([sameDevice]: Leave the Bridge) or the same
     * address spelled otherwise keeps it for [after], so its first sign-in
     * there is already pinned; another address starts without one (null).
     */
    fun homeAfterSave(home: Pair<String, HomeEndpoint>?, before: String, after: String, sameDevice: Boolean): Pair<String, HomeEndpoint>? {
        if (home == null) return null
        if (home.first == after) return home
        val moves = sameDevice || (before.isNotEmpty() && SecretsStore.sameAddress(before, after))
        return if (moves && home.first == before) after to home.second else null
    }

    /** [url]'s host, brackets and all for IPv6 (HomeEndpoint.including takes them off); null when it has none. */
    fun host(url: String): String? = try { URI(url.trim()).host } catch (e: Exception) { null }

    /** What answered at a pinned ws:// address when it was asked for its key alone ([probe]). */
    sealed interface Probe {
        /** Nothing answered, or not in time: the phone isn't where the device is. */
        class Failed(val error: Exception) : Probe
        /** A device that can't reach its storage (issue #207), which serves no home listener without it. */
        data object Storage : Probe
        /** Anything else (a key, an Ack): the network is up. */
        data object Answered : Probe
    }

    /**
     * Asks whatever answers at [url] for its key only - GetPubKey carries
     * nothing of this phone's - and closes the socket: never Auth, never the
     * password. [client] is a fresh socket of its own.
     */
    suspend fun probe(url: String, timeoutMs: Long = PROBE_TIMEOUT_MS, client: WSClient = WSClient()): Probe {
        try {
            client.connect(url)
            val resp = client.request(timeoutMs) { it.setReqGetPubKey(GetPubKey.getDefaultInstance()) }
            return if (ErrorCodes.isStorageUnavailable(resp)) Probe.Storage else Probe.Answered
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            return Probe.Failed(e)
        } finally {
            client.close()
        }
    }

    /** How the pinned link of a pinned ws:// address was looked for ([find]). */
    sealed interface Find {
        /** Opened, the pin checked: sign in over it. */
        class Found(val opened: HomeNetwork.Opened) : Find
        /** Neither the link nor the address answered: [error] says why, as for any address. */
        class Unreachable(val error: Exception) : Find
        /** The address answered storage_unavailable (issue #207): the app's own words, never the answer's (nothing proves it is the device). */
        data object Storage : Find
        /** The address answers, and a certificate other than the pin was shown at the device's port: something else holds the address. */
        data object NotKnownDevice : Find
        /** The address answers, the device's encrypted [port] doesn't (a firewall, a container not publishing it, its listener stopped). */
        class HomePortUnreachable(val port: Int) : Find
    }

    /**
     * The pinned link of a pinned ws:// address, or why it can't be had - in
     * no case is the address itself signed in to. [race] races the targets
     * within its budget: first [firstMs] (the connect's usual race); nothing
     * won, [probe] asks the address what it is; when anything answers there
     * the network is up, and the link is raced once more for [secondMs] (a
     * Wi-Fi just waking, a busy device). [port]: the pinned port, for the
     * words.
     */
    suspend fun find(
        port: Int,
        race: suspend (budgetMs: Long) -> HomeNetwork.Contest,
        probe: suspend () -> Probe,
        firstMs: Long = HomeNetwork.BUDGET_MS,
        secondMs: Long = HomeNetwork.SECOND_BUDGET_MS,
    ): Find {
        val first = race(firstMs)
        first.opened?.let { return Find.Found(it) }
        when (val p = probe()) {
            is Probe.Failed -> return Find.Unreachable(p.error)
            Probe.Storage -> return Find.Storage
            Probe.Answered -> {}
        }
        val second = race(secondMs)
        second.opened?.let { return Find.Found(it) }
        return if (first.otherCertificate || second.otherCertificate) Find.NotKnownDevice else Find.HomePortUnreachable(port)
    }
}
