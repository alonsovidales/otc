// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.util.Log
import cloud.offthe.otc.OTCApp
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

// Issue #190: the phone changing networks (home Wi-Fi to mobile data, or
// back) is when the way to the device may change; OTCConnection then picks
// the route again.
object NetworkWatch {
    private const val TAG = "OTC/NetworkWatch"
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    @Volatile private var last: Network? = null
    @Volatile private var pending: Job? = null
    @Volatile private var started = false

    fun start(ctx: Context) {
        if (started) return
        started = true
        val cm = ctx.getSystemService(ConnectivityManager::class.java) ?: return
        last = cm.activeNetwork
        try {
            cm.registerDefaultNetworkCallback(object : ConnectivityManager.NetworkCallback() {
                // A new default network; losing one without a replacement
                // leaves nothing to switch to.
                override fun onAvailable(network: Network) {
                    if (network == last) return
                    last = network
                    // Settled first: a handover reports several in a row.
                    pending?.cancel()
                    pending = scope.launch {
                        delay(1_500)
                        // Back online (or on another network): what failed
                        // meanwhile is asked for again now (Wake).
                        Wake.fire()
                        OTCConnection.reconsiderRoute()
                    }
                }

                // Gone with nothing in its place (airplane mode, Wi-Fi off
                // with no mobile data): the same network coming back is
                // news too, and wakes what failed meanwhile.
                override fun onLost(network: Network) {
                    if (network == last) last = null
                }
            })
        } catch (e: Exception) {
            // Too many callbacks registered (a system limit): the next
            // reconnect still picks the route.
            Log.w(TAG, "can't watch the network: $e")
        }
    }

    /**
     * The ways a home connection may go out: the Wi-Fi and Ethernet
     * networks the phone is on, bound to explicitly (with the home internet
     * down, Android makes mobile data the default and an unbound socket
     * never reaches the device), and the default network unless it is one
     * of those or mobile data alone - where no home address answers and
     * the attempt would only add its 2.5 s to every connect away from home.
     * A VPN may route home, so it is tried. Empty: don't try.
     */
    fun homeRoutes(): List<HomeNetwork.Via> {
        val cm = OTCApp.instance.getSystemService(ConnectivityManager::class.java) ?: return listOf(HomeNetwork.Via.DEFAULT)
        return try {
            val active = cm.activeNetwork
            val lan = ArrayList<Network>()
            @Suppress("DEPRECATION") // the plain list is all this needs
            for (n in cm.allNetworks) {
                val caps = cm.getNetworkCapabilities(n) ?: continue
                if (caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) continue
                if (caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) || caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET)) lan += n
            }
            val caps = active?.let { cm.getNetworkCapabilities(it) }
            val tryDefault = active !in lan && tryDefault(
                cellular = caps?.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) == true,
                vpn = caps?.hasTransport(NetworkCapabilities.TRANSPORT_VPN) == true,
            )
            lan.map { HomeNetwork.Via("net:$it", it.socketFactory) } + if (tryDefault) listOf(HomeNetwork.Via.DEFAULT) else emptyList()
        } catch (e: Exception) {
            listOf(HomeNetwork.Via.DEFAULT)
        }
    }

    fun tryDefault(cellular: Boolean, vpn: Boolean) = !cellular || vpn

    /**
     * Whether the phone is on a network that leads anywhere at all: false
     * with none (airplane mode, Wi-Fi and mobile data off), which is what a
     * screen then says rather than blaming the device. True when that can't
     * be told.
     */
    fun online(): Boolean = try {
        val cm = OTCApp.instance.getSystemService(ConnectivityManager::class.java)
        if (cm == null) true
        else cm.activeNetwork?.let { cm.getNetworkCapabilities(it)?.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) } == true
    } catch (e: Exception) {
        true
    }
}
