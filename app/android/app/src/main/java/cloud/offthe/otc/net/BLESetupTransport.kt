// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.net

import android.Manifest
import android.annotation.SuppressLint
import android.bluetooth.BluetoothAdapter
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothGatt
import android.bluetooth.BluetoothGattCallback
import android.bluetooth.BluetoothGattCharacteristic
import android.bluetooth.BluetoothGattDescriptor
import android.bluetooth.BluetoothManager
import android.bluetooth.BluetoothProfile
import android.bluetooth.le.ScanCallback
import android.bluetooth.le.ScanFilter
import android.bluetooth.le.ScanResult
import android.bluetooth.le.ScanSettings
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import android.os.ParcelUuid
import androidx.core.content.ContextCompat
import java.io.ByteArrayOutputStream
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap
import java.util.zip.Inflater
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withTimeout
import org.json.JSONObject

/**
 * Port of BLESetupTransport (BluetoothSetupView.swift), issue #137: finds
 * the device advertising the setup service, connects, and turns
 * [request] calls into chunked writes with the answer reassembled from
 * notifications. Wire format as scripts/setup_ble.py: chunk = stream id,
 * flags (bit 0 = last), payload; request = JSON {m, p, b}; answer = raw
 * DEFLATE of JSON {s, t, b}.
 */
class BLESetupTransport(private val context: Context) {

    sealed class Phase {
        object Starting : Phase()
        object Off : Phase()
        object Unauthorized : Phase()
        object Scanning : Phase()
        object Connecting : Phase()
        data class Ready(val name: String) : Phase()
        object Lost : Phase()
    }

    class Answer(val status: Int, val contentType: String, val body: String)

    class SetupException(message: String) : Exception(message)

    private val _phase = MutableStateFlow<Phase>(Phase.Starting)
    val phase: StateFlow<Phase> = _phase

    /** From the wizard's /api/state once the install is online: the domain, "" without the bridge. */
    private val _readyDomain = MutableStateFlow<String?>(null)
    val readyDomain: StateFlow<String?> = _readyDomain

    /** The owner password chosen in the wizard, handed over by its page (memory only). */
    val chosenPassword = MutableStateFlow("")

    /** The device came from a recovered array: it keeps its password. */
    private val _readyRecovery = MutableStateFlow(false)
    val readyRecovery: StateFlow<Boolean> = _readyRecovery

    /** Whether the wizard has been reached once: a dropped link then keeps the page (as iOS). */
    private val _everReady = MutableStateFlow(false)
    val everReady: StateFlow<Boolean> = _everReady

    private val adapter: BluetoothAdapter? =
        (context.getSystemService(Context.BLUETOOTH_SERVICE) as? BluetoothManager)?.adapter
    private var gatt: BluetoothGatt? = null
    private var requestChrc: BluetoothGattCharacteristic? = null
    private var responseChrc: BluetoothGattCharacteristic? = null
    private val partial = HashMap<Int, ByteArrayOutputStream>()
    private val waiters = ConcurrentHashMap<Int, CompletableDeferred<ByteArray>>()
    private var nextStream = 0
    @Volatile private var mtu = 23
    private val writeLock = Mutex()
    @Volatile private var writeDone: CompletableDeferred<Boolean>? = null
    private var scanning = false
    private var stopped = false

    val isReady: Boolean get() = _phase.value is Phase.Ready

    companion object {
        /**
         * Sent with every request: a device being set up answers only the
         * first phone that talked to it - this key, not the phone's
         * Bluetooth address, which changes every few minutes. Kept for the
         * life of the app install, so reopening the app mid-setup works.
         */
        fun setupKey(context: Context): String {
            val prefs = context.getSharedPreferences("otc_setup", Context.MODE_PRIVATE)
            prefs.getString("setupKey", null)?.takeIf { it.isNotEmpty() }?.let { return it }
            val bytes = ByteArray(24).also { java.security.SecureRandom().nextBytes(it) }
            val key = bytes.joinToString("") { "%02x".format(it) }
            prefs.edit().putString("setupKey", key).apply()
            return key
        }

        val SERVICE: UUID = UUID.fromString("0f7c5e70-0b1e-4b8a-9c2d-5e7a1c0d0001")
        val REQUEST: UUID = UUID.fromString("0f7c5e70-0b1e-4b8a-9c2d-5e7a1c0d0002")
        val RESPONSE: UUID = UUID.fromString("0f7c5e70-0b1e-4b8a-9c2d-5e7a1c0d0003")
        val INFO: UUID = UUID.fromString("0f7c5e70-0b1e-4b8a-9c2d-5e7a1c0d0004")
        private val CCCD: UUID = UUID.fromString("00002902-0000-1000-8000-00805f9b34fb")

        /** The runtime permissions this needs on this Android version. */
        fun permissions(): Array<String> =
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) arrayOf(Manifest.permission.BLUETOOTH_SCAN, Manifest.permission.BLUETOOTH_CONNECT)
            else arrayOf(Manifest.permission.ACCESS_FINE_LOCATION)

        fun hasPermissions(context: Context): Boolean =
            permissions().all { ContextCompat.checkSelfPermission(context, it) == PackageManager.PERMISSION_GRANTED }
    }

    @SuppressLint("MissingPermission")
    fun start() {
        stopped = false
        if (!hasPermissions(context)) { _phase.value = Phase.Unauthorized; return }
        val a = adapter
        if (a == null || !a.isEnabled) { _phase.value = Phase.Off; return }
        scan()
    }

    @SuppressLint("MissingPermission")
    fun stop() {
        stopped = true
        if (scanning) { adapter?.bluetoothLeScanner?.stopScan(scanCallback); scanning = false }
        gatt?.close()
        gatt = null
        failAll(SetupException("Setup was closed"))
    }

    @SuppressLint("MissingPermission")
    private fun scan() {
        val scanner = adapter?.bluetoothLeScanner ?: run { _phase.value = Phase.Off; return }
        _phase.value = Phase.Scanning
        scanning = true
        scanner.startScan(
            listOf(ScanFilter.Builder().setServiceUuid(ParcelUuid(SERVICE)).build()),
            ScanSettings.Builder().setScanMode(ScanSettings.SCAN_MODE_LOW_LATENCY).build(),
            scanCallback,
        )
    }

    private val scanCallback = object : ScanCallback() {
        @SuppressLint("MissingPermission")
        override fun onScanResult(callbackType: Int, result: ScanResult) {
            if (gatt != null || stopped) return
            adapter?.bluetoothLeScanner?.stopScan(this)
            scanning = false
            _phase.value = Phase.Connecting
            gatt = result.device.connectGatt(context, false, gattCallback, BluetoothDevice.TRANSPORT_LE)
        }

        override fun onScanFailed(errorCode: Int) { _phase.value = Phase.Off }
    }

    private val gattCallback = object : BluetoothGattCallback() {
        @SuppressLint("MissingPermission")
        override fun onConnectionStateChange(g: BluetoothGatt, status: Int, newState: Int) {
            if (newState == BluetoothProfile.STATE_CONNECTED) {
                g.requestMtu(517)
            } else if (newState == BluetoothProfile.STATE_DISCONNECTED) {
                g.close()
                if (gatt === g) gatt = null
                requestChrc = null
                responseChrc = null
                failAll(SetupException("Not connected to the device"))
                _phase.value = Phase.Lost
                if (!stopped) scan()
            }
        }

        @SuppressLint("MissingPermission")
        override fun onMtuChanged(g: BluetoothGatt, newMtu: Int, status: Int) {
            if (status == BluetoothGatt.GATT_SUCCESS) mtu = newMtu
            g.discoverServices()
        }

        @SuppressLint("MissingPermission")
        override fun onServicesDiscovered(g: BluetoothGatt, status: Int) {
            val service = g.getService(SERVICE) ?: return
            requestChrc = service.getCharacteristic(REQUEST)
            val resp = service.getCharacteristic(RESPONSE) ?: return
            responseChrc = resp
            g.setCharacteristicNotification(resp, true)
            val cccd = resp.getDescriptor(CCCD) ?: return
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                g.writeDescriptor(cccd, BluetoothGattDescriptor.ENABLE_NOTIFICATION_VALUE)
            } else {
                @Suppress("DEPRECATION")
                cccd.value = BluetoothGattDescriptor.ENABLE_NOTIFICATION_VALUE
                @Suppress("DEPRECATION")
                g.writeDescriptor(cccd)
            }
        }

        @SuppressLint("MissingPermission")
        override fun onDescriptorWrite(g: BluetoothGatt, descriptor: BluetoothGattDescriptor, status: Int) {
            if (descriptor.characteristic.uuid == RESPONSE && requestChrc != null) {
                _phase.value = Phase.Ready(g.device.name ?: "Off The Cloud")
                _everReady.value = true
                g.getService(SERVICE)?.getCharacteristic(INFO)?.let { g.readCharacteristic(it) }
            }
        }

        override fun onCharacteristicWrite(g: BluetoothGatt, characteristic: BluetoothGattCharacteristic, status: Int) {
            writeDone?.complete(status == BluetoothGatt.GATT_SUCCESS)
        }

        @SuppressLint("MissingPermission")
        override fun onServiceChanged(g: BluetoothGatt) {
            // The device's service went away or came back: start over.
            g.disconnect()
        }

        @Deprecated("Deprecated in Java")
        override fun onCharacteristicRead(g: BluetoothGatt, characteristic: BluetoothGattCharacteristic, status: Int) {
            if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
                @Suppress("DEPRECATION")
                onCharacteristicRead(g, characteristic, characteristic.value ?: ByteArray(0), status)
            }
        }

        override fun onCharacteristicRead(g: BluetoothGatt, characteristic: BluetoothGattCharacteristic, value: ByteArray, status: Int) {
            if (characteristic.uuid == INFO && status == BluetoothGatt.GATT_SUCCESS) {
                runCatching { JSONObject(String(value)).optString("name") }.getOrNull()?.takeIf { it.isNotEmpty() }?.let {
                    if (isReady) _phase.value = Phase.Ready(it)
                }
            }
        }

        @Deprecated("Deprecated in Java")
        override fun onCharacteristicChanged(g: BluetoothGatt, characteristic: BluetoothGattCharacteristic) {
            if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
                @Suppress("DEPRECATION")
                onCharacteristicChanged(g, characteristic, characteristic.value ?: ByteArray(0))
            }
        }

        override fun onCharacteristicChanged(g: BluetoothGatt, characteristic: BluetoothGattCharacteristic, value: ByteArray) {
            if (characteristic.uuid != RESPONSE || value.size < 2) return
            val stream = value[0].toInt() and 0xff
            val last = value[1].toInt() and 1 == 1
            val message: ByteArray? = synchronized(partial) {
                val buf = partial.getOrPut(stream) { ByteArrayOutputStream() }
                buf.write(value, 2, value.size - 2)
                if (last) { partial.remove(stream); buf.toByteArray() } else null
            }
            if (message != null) waiters.remove(stream)?.complete(message)
        }
    }

    private fun failAll(e: Exception) {
        val pending = waiters.values.toList()
        waiters.clear()
        synchronized(partial) { partial.clear() }
        pending.forEach { it.completeExceptionally(e) }
    }

    /** One request to the wizard, over Bluetooth. Safe to call concurrently. */
    @SuppressLint("MissingPermission")
    suspend fun request(method: String, path: String, body: String?): Answer {
        val g = gatt
        val chrc = requestChrc
        if (g == null || chrc == null || !isReady) throw SetupException("Not connected to the device")
        val stream = synchronized(this) { nextStream = (nextStream + 1) and 0xff; nextStream }
        val msg = JSONObject().put("m", method).put("p", path).put("k", setupKey(context))
        if (!body.isNullOrEmpty()) msg.put("b", body)
        // The biggest notification this phone takes whole: MTU-3, and
        // never more than the 512 bytes an attribute value can hold.
        msg.put("c", minOf(512, mtu - 3))
        val payload = msg.toString().toByteArray(Charsets.UTF_8)
        val waiter = CompletableDeferred<ByteArray>()
        waiters[stream] = waiter
        // Android takes one write at a time: wait for its callback before
        // the next chunk, and let requests take turns at the characteristic.
        val size = maxOf(18, minOf(512, mtu - 3) - 2)
        writeLock.withLock {
            var offset = 0
            do {
                val end = minOf(offset + size, payload.size)
                val chunk = ByteArray(2 + end - offset)
                chunk[0] = stream.toByte()
                chunk[1] = if (end >= payload.size) 1 else 0
                System.arraycopy(payload, offset, chunk, 2, end - offset)
                val done = CompletableDeferred<Boolean>()
                writeDone = done
                val ok = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                    g.writeCharacteristic(chrc, chunk, BluetoothGattCharacteristic.WRITE_TYPE_DEFAULT) == BluetoothGatt.GATT_SUCCESS
                } else {
                    @Suppress("DEPRECATION")
                    chrc.value = chunk
                    @Suppress("DEPRECATION")
                    g.writeCharacteristic(chrc)
                }
                if (!ok) { waiters.remove(stream); throw SetupException("Could not send to the device") }
                // A failed write means the device's service is gone while
                // the link stays up (its daemon restarting, as iOS):
                // drop the link so it reconnects instead of waiting.
                val written = runCatching { withTimeout(15_000) { done.await() } }.getOrDefault(false)
                if (!written) {
                    waiters.remove(stream)
                    g.disconnect()
                    throw SetupException("Lost the device - reconnecting")
                }
                offset = end
            } while (offset < payload.size)
        }
        val raw = try {
            withTimeout(90_000) { waiter.await() }
        } catch (e: Exception) {
            waiters.remove(stream)
            throw if (e is SetupException) e else SetupException("The device did not answer in time")
        }
        val json = try { JSONObject(String(inflate(raw), Charsets.UTF_8)) } catch (e: Exception) {
            throw SetupException("The device sent an answer this app could not read")
        }
        val answer = Answer(json.optInt("s", 502), json.optString("t", "application/octet-stream"), json.optString("b", ""))
        if (path.startsWith("/api/state") && answer.status == 200) noteState(answer.body)
        return answer
    }

    /** The wizard's own state tells when the device is ready for the app. */
    private fun noteState(body: String) {
        val st = runCatching { JSONObject(body) }.getOrNull() ?: return
        val install = st.optJSONObject("install") ?: return
        if (install.optString("phase") != "online") return
        val domain = install.optString("domain").ifEmpty { st.optString("domain") }
        if (_readyDomain.value != domain) _readyDomain.value = domain
        _readyRecovery.value = install.optBoolean("recovery", false)
    }

    private fun inflate(data: ByteArray): ByteArray {
        val inf = Inflater(true) // raw deflate, no zlib header
        inf.setInput(data)
        val out = ByteArrayOutputStream()
        val buf = ByteArray(8192)
        while (!inf.finished()) {
            val n = inf.inflate(buf)
            if (n == 0 && (inf.needsInput() || inf.needsDictionary())) break
            out.write(buf, 0, n)
        }
        inf.end()
        return out.toByteArray()
    }
}
