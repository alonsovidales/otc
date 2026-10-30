// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import android.content.Context
import cloud.offthe.otc.OTCApp
import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appSettingsSmartSearchDownloadNote
import cloud.offthe.otc.i18n.appSettingsSmartSearchDownloading
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.GetSettings
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.Settings
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged

/**
 * Where Smart search's model is, as the device's Settings say
 * (Settings.smart_search_model). Since release 152 the device downloads it,
 * about 400 MB, only once Smart search is first turned on. A device before
 * that says nothing - it had the model, or refuses turning Smart search on -
 * and is [Ready]: everything as before. iOS's SmartSearchModelState.
 */
sealed interface SmartSearchModelState {
    /** On the device: Smart search works. */
    data object Ready : SmartSearchModelState

    /** Not on the device, and nothing asked for it: turning Smart search on downloads it. */
    data object Missing : SmartSearchModelState

    /**
     * Being downloaded, [percent] 0-100 - or waiting to try again, with
     * [problem] saying why in the device's words (it tries again by itself).
     */
    data class Downloading(val percent: Int, val problem: String? = null) : SmartSearchModelState

    val isDownloading: Boolean get() = this is Downloading

    /**
     * What Settings' section says under its footer: the download note while
     * the model isn't there, how far the download is - or the device's words
     * for why it waits - while it comes, nothing once it is there.
     */
    fun words(): UiText? = when (this) {
        Ready -> null
        Missing -> S.appSettingsSmartSearchDownloadNote()
        is Downloading -> problem?.let { UiText.Raw(it) } ?: S.appSettingsSmartSearchDownloading(percent)
    }

    /** The progress bar's share (0-1) while downloading; null: no bar. */
    val progress: Float? get() = (this as? Downloading)?.let { it.percent / 100f }

    companion object {
        /** From the device's Settings; absent (a device before release 152): [Ready]. */
        fun of(s: Settings): SmartSearchModelState {
            if (!s.hasSmartSearchModel()) return Ready
            val m = s.smartSearchModel
            return when {
                m.ready -> Ready
                m.downloading -> Downloading(m.percent.coerceIn(0, 100), m.problem.trim().ifEmpty { null })
                else -> Missing
            }
        }

        /**
         * What TalkBack is told as Settings' section changes (a polite live
         * region): when the download starts (or starts again after a
         * problem), runs into a problem, and is done (as 100%) - never at
         * every percent. null: nothing new to say. [before] is null for the
         * first view, which isn't announced. The web's announcement().
         */
        fun announcement(before: SmartSearchModelState?, now: SmartSearchModelState): SmartSearchAnnouncement? {
            if (before == null) return null
            if (now is Downloading) {
                val problem = now.problem
                if (problem != null) return if (before is Downloading && before.problem == problem) null else SmartSearchAnnouncement.Problem(problem)
                return if (before is Downloading && before.problem == null) null else SmartSearchAnnouncement.Progress(now.percent)
            }
            if (now == Ready && before is Downloading) return SmartSearchAnnouncement.Progress(100)
            return null
        }
    }
}

/** What Settings' Smart search section has TalkBack say ([SmartSearchModelState.announcement]). */
sealed interface SmartSearchAnnouncement {
    /** The download started (again) at [percent], or is done (100). */
    data class Progress(val percent: Int) : SmartSearchAnnouncement
    /** Why the download waits, in the device's words. */
    data class Problem(val text: String) : SmartSearchAnnouncement

    fun text(): UiText = when (this) {
        is Progress -> S.appSettingsSmartSearchDownloading(percent)
        is Problem -> UiText.Raw(text)
    }
}

/**
 * Whether Smart search is on (finding photos by a description typed in the
 * search): the signed-in device has it (release 151) and it is on for this
 * user (Settings.smart_search_enabled: off by default in 151, on by default
 * from 152 - never assume either, the device says) - and, with it, where
 * its model is ([model], release 152). Told by the device's Settings -
 * asked once after sign-in with face recognition's (FaceRecognition.watch,
 * the same answer), whenever Settings loads (then every [POLL_EVERY_MS]
 * while the model downloads and Settings shows), and when Images' search
 * gets the focus while the model isn't there ([searchFocused]) -, by Settings'
 * switch, and by a search told "smart_search_off" (turned off on another
 * app) or "smart_search_downloading". PlacesSupport's twin. Only while it is
 * [searchable] does the search offer a text search, and only then is
 * SearchPhotos.text sent: a device before it would ignore the text and
 * answer the whole library.
 *
 * Until the device has answered, the switch this phone had last time
 * stands in (the same prefs file as face recognition's). null (never
 * answered, or Log Out): off. The model isn't kept: [SmartSearchModelState.Ready]
 * until the device says otherwise.
 */
object SmartSearchSupport {
    private const val PREFS = "otc_device_state"
    private const val KEY = "smart_search"

    /** How often Settings asks where the model is while it downloads. */
    const val POLL_EVERY_MS = 3_000L

    /** Images' search asks where the model is, as it gets the focus, at most this often. */
    const val FOCUS_ASK_EVERY_MS = 5_000L

    const val OFF = "smart_search_off"
    const val UNAVAILABLE = "smart_search_unavailable"
    const val DOWNLOADING = "smart_search_downloading"

    private fun prefs() = OTCApp.instance.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    private val _enabled = MutableStateFlow(remembered())
    /** true or false as the device said (or last said here); null: not known yet. False too on a device before it. */
    val enabled: StateFlow<Boolean?> = _enabled

    private val _model = MutableStateFlow<SmartSearchModelState>(SmartSearchModelState.Ready)
    /** Where the model is: Ready for a device before release 152, and after Log Out. */
    val model: StateFlow<SmartSearchModelState> = _model

    private fun remembered(): Boolean? = try {
        val p = prefs()
        if (p.contains(KEY)) p.getBoolean(KEY, false) else null
    } catch (_: Exception) { null }

    /** The free-text search is offered: on, with the model there. */
    fun searchable(on: Boolean?, model: SmartSearchModelState): Boolean = on == true && model == SmartSearchModelState.Ready

    /** [searchable] now. */
    val searchable: Boolean get() = searchable(enabled.value, model.value)

    /** [searchable] as it changes. */
    val searchableFlow: Flow<Boolean> get() = combine(enabled, model) { on, m -> searchable(on, m) }.distinctUntilChanged()

    /**
     * Settings asks again (every [POLL_EVERY_MS]) while the switch is on and
     * the model is on its way: not once it is there, nor with the switch off.
     */
    fun polls(on: Boolean?, model: SmartSearchModelState): Boolean = on == true && model.isDownloading

    /**
     * Whether Images' search getting the focus asks where the model is: on,
     * the model not there yet, and not asked in the last [FOCUS_ASK_EVERY_MS]
     * ([lastAskedMs] null: never) - so the Smart row comes once the model is
     * there, without a visit to Settings.
     */
    fun asksOnFocus(on: Boolean?, model: SmartSearchModelState, lastAskedMs: Long?, nowMs: Long): Boolean =
        on == true && model != SmartSearchModelState.Ready && (lastAskedMs == null || nowMs - lastAskedMs >= FOCUS_ASK_EVERY_MS)

    private var focusAskedAt: Long? = null

    /** Images' search got the focus: asks the device's Settings when [asksOnFocus]. */
    suspend fun searchFocused(nowMs: Long = System.nanoTime() / 1_000_000, ask: suspend () -> Unit = { refresh() }) {
        if (!asksOnFocus(enabled.value, model.value, focusAskedAt, nowMs)) return
        focusAskedAt = nowMs
        ask()
    }

    /** What the device's Settings say: on or off - false for a device before Smart search (no smart_search_enabled). */
    fun verdict(s: Settings): Boolean = s.hasSmartSearchEnabled() && s.smartSearchEnabled

    /** A Settings answer: the switch and where the model is. */
    fun deviceSaid(s: Settings) {
        set(verdict(s))
        _model.value = SmartSearchModelState.of(s)
    }

    /** The device's answer: its Settings, Settings' switch acknowledged, or a search told "smart_search_off". */
    fun set(on: Boolean) {
        try { prefs().edit().putBoolean(KEY, on).apply() } catch (_: Exception) {}
        _enabled.value = on
    }

    /**
     * Turned on where the model isn't, or a search told
     * "smart_search_downloading": on its way, until the device says more.
     */
    fun modelRequested() {
        if (!_model.value.isDownloading) _model.value = SmartSearchModelState.Downloading(0)
    }

    /** Asks the device's Settings, with no effect here; null when they didn't come. */
    suspend fun askSettings(): Settings? = try {
        val resp = OTCConnection.request { it.setReqGetSettings(GetSettings.getDefaultInstance()) }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_SETTINGS) resp.respSettings else null
    } catch (e: CancellationException) {
        throw e
    } catch (_: Exception) { null }

    /** Asks the device's Settings again: the switch, and where the model is. Returns them, or null when they didn't come. */
    suspend fun refresh(): Settings? = askSettings()?.also { deviceSaid(it) }

    /** Why the device refused a search's words. */
    enum class Refused {
        /** Turned off (on another app): the switch goes off here too. */
        OFF,
        /** The device can't search by description: the switch stays. */
        UNAVAILABLE,
        /** The model is still on its way (release 152): the switch stays, and the device is asked where the model is. */
        DOWNLOADING,
    }

    /** A refusal: the device's words (null when it gave none), and why. */
    data class Refusal(val words: String?, val why: Refused)

    /** A SearchPhotos answer that refuses the text; null for any other answer. */
    fun refusal(resp: RespEnvelope?): Refusal? {
        if (resp == null || !resp.error) return null
        val why = when (resp.errorCode) {
            OFF -> Refused.OFF
            UNAVAILABLE -> Refused.UNAVAILABLE
            DOWNLOADING -> Refused.DOWNLOADING
            else -> return null
        }
        return Refusal(resp.errorMessage.ifEmpty { null }, why)
    }

    /** Log Out: the next device says for itself. */
    fun reset() {
        try { prefs().edit().remove(KEY).apply() } catch (_: Exception) {}
        _enabled.value = null
        _model.value = SmartSearchModelState.Ready
        focusAskedAt = null
    }
}
