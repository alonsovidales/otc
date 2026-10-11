// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.i18n

import android.app.Activity
import android.content.Context
import android.content.SharedPreferences
import android.content.res.Configuration
import android.os.Build
import android.os.LocaleList
import android.util.Log
import androidx.appcompat.app.AppCompatDelegate
import androidx.core.app.LocaleManagerCompat
import androidx.core.content.edit
import androidx.core.os.LocaleListCompat
import cloud.offthe.otc.net.ErrorCodes
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.GetSettings
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.proto.SetLanguage
import cloud.offthe.otc.proto.Settings
import cloud.offthe.otc.proto.Status
import cloud.offthe.otc.push.FCMPush
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import java.util.Locale

// The language the app shows (docs/i18n.md, "The stored choice"). The
// user's choice lives on the device, per user - Settings.language and
// Status.language: "" for Automatic, absent on a device that predates it -
// and every app follows it. This phone keeps a copy of its own (the prefs
// file otc_language, which Log Out leaves alone and backup leaves out) for
// the time before the device has said, or a device that can't: the copy is
// what the app shows, and the device's value, once seen, becomes the copy.
// The iOS app's LanguageSettings.swift is the same.
//
// Android shows it through AppCompat's per-app locales (spike S2): a change
// made here calls AppCompatDelegate.setApplicationLocales with the
// language's full tag (Languages.kt: "pt-PT", never "pt"; Automatic is the
// empty list) and the Activity is recreated. Only MainActivity and the
// Language screen write it; a change that comes from the device is applied
// at MainActivity's next start, never in the middle of a form. Text built
// outside an Activity (the notification channel) goes through
// localizedContext. A device too old to keep it (unknown_payload) leaves the
// change pending and nothing more is sent until the next connection, when
// the device may have been updated.

/** The choice and what the device's answers do to it, as plain values. */
data class LanguageChoice(
    /** The local copy: "" (Automatic) or a language code - possibly one this build doesn't have, which shows English and is kept as chosen. */
    val language: String = "",
    /** Chosen on this phone and not yet taken by the device: sent again at the next connection. */
    val pending: Boolean = false,
    /**
     * When it was chosen (or last sent), in ms; 0 for none. For [WINDOW_MS]
     * after it, a different value from the device is an answer given before
     * ours was applied, and ignored.
     */
    val pendingAt: Long = 0L,
    /** The device's value this phone last saw, SetLanguage's expected; null when it never saw one. */
    val seen: String? = null,
) {
    /** Chosen on this phone: shown at once, and pending until the device takes it. */
    fun chosen(code: String, nowMs: Long) = copy(language = code, pending = true, pendingAt = nowMs)

    /** Being sent: the window counts from now. */
    fun sending(nowMs: Long) = copy(pendingAt = nowMs)

    /** The device took [sent]. */
    fun taken(sent: String) = copy(seen = sent, pending = pending && language != sent)

    /**
     * The device refused [sent] because its value had changed meanwhile (Ack
     * code "changed"), and holds [value]: adopted, unless this phone has
     * chosen again since - that one goes next, expecting [value].
     */
    fun refused(sent: String, value: String) =
        if (language == sent) copy(language = value, pending = false, pendingAt = 0L, seen = value)
        else copy(seen = value)

    /**
     * The device's value, from its Status or Settings (null: a device that
     * predates the field): adopted, unless it is an older answer to a change
     * of ours still within [WINDOW_MS].
     */
    fun deviceSaid(value: String?, nowMs: Long): LanguageChoice {
        if (value == null) return this
        // Ours, seen: nothing left to wait for.
        if (value == language) return copy(seen = value, pending = false, pendingAt = 0L)
        if (pendingAt != 0L) {
            val age = nowMs - pendingAt
            if (age in 0 until WINDOW_MS) return this
        }
        return copy(language = value, seen = value, pending = false, pendingAt = 0L)
    }

    /** Log Out: the copy stays; what belonged to the device signed in to goes. */
    fun loggedOut() = copy(pending = false, pendingAt = 0L, seen = null)

    companion object {
        const val WINDOW_MS = 30_000L

        /**
         * The language a choice shows: Automatic is the first of the system's
         * languages this build has, matched by base language ([ca-ES, es-ES]
         * gives es, pt-BR gives pt), else English; a code this build doesn't
         * have is English.
         */
        fun effective(language: String, system: List<Locale>, languages: List<Language> = Languages.all): String =
            if (language.isEmpty()) matchSystem(system, languages)
            else languages.firstOrNull { it.code == language }?.code ?: Languages.SOURCE

        /** The system's language among [languages], by base language; English when none matches. Never the pseudo-locale. */
        fun matchSystem(system: List<Locale>, languages: List<Language> = Languages.all): String {
            for (locale in system) {
                languages.firstOrNull { !isPseudo(it) && Locale.forLanguageTag(it.tag).language == locale.language }
                    ?.let { return it.code }
            }
            return Languages.SOURCE
        }

        /** The pseudo-locale of draft builds (tag en-Qaaa): chosen by hand only. */
        fun isPseudo(l: Language) = Locale.forLanguageTag(l.tag).script.equals("Qaaa", ignoreCase = true)

        /**
         * The tag to hand Android for a choice: "" (Automatic: the system's
         * list), the language's full tag, or English's for a code this build
         * doesn't have.
         */
        fun tagFor(language: String, languages: List<Language> = Languages.all): String =
            if (language.isEmpty()) ""
            else (languages.firstOrNull { it.code == language } ?: languages.first { it.code == Languages.SOURCE }).tag

        /** The language behind Android's tags ("de-AT" is de); "" for none, or none this build has. */
        fun codeFor(tags: String, languages: List<Language> = Languages.all): String {
            if (tags.isEmpty()) return ""
            val first = Locale.forLanguageTag(tags.substringBefore(','))
            return languages.firstOrNull { it.tag.equals(first.toLanguageTag(), ignoreCase = true) }?.code
                ?: languages.firstOrNull { !isPseudo(it) && Locale.forLanguageTag(it.tag).language == first.language }?.code
                ?: ""
        }

        /**
         * Whether Android already shows [want]: the same tags, or the same
         * language in another region (a pick in the system's screen) - but
         * not another script (the pseudo-locale en-Qaaa isn't English).
         */
        fun showsAlready(platform: String, want: String): Boolean {
            if (platform == want) return true
            if (platform.isEmpty() || want.isEmpty()) return false
            val p = Locale.forLanguageTag(platform.substringBefore(','))
            val w = Locale.forLanguageTag(want)
            return p.language == w.language && p.script == w.script
        }
    }
}

/** What the device answered to SetLanguage. */
enum class LanguageAnswer {
    TAKEN, CHANGED, TOO_OLD, FAILED;

    companion object {
        fun of(resp: RespEnvelope): LanguageAnswer {
            if (resp.error) return if (ErrorCodes.isUnknownPayload(resp.errorCode, resp.errorMessage)) TOO_OLD else FAILED
            if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_ACK) return FAILED
            val ack = resp.respAck
            return when {
                ack.ok -> TAKEN
                ack.code == "changed" -> CHANGED
                ack.code == "unknown_payload" -> TOO_OLD
                else -> FAILED
            }
        }
    }
}

/** The app's choice of language: kept here, shown through AppCompat, told to the device. */
object LanguageSettings {
    /** The prefs file: outside SecretsStore's Log Out wipe and outside backup (data_extraction_rules.xml lists otc_settings.xml only). */
    const val PREFS = "otc_language"
    private const val KEY_LANGUAGE = "language"
    private const val KEY_PENDING = "pending"
    private const val KEY_PENDING_AT = "pendingAt"
    private const val KEY_SEEN = "seen"
    /** The tags last handed to Android ("" for the system's), and the API level then. */
    private const val KEY_APPLIED = "applied"
    private const val KEY_APPLIED_SDK = "appliedSdk"
    private const val TAG = "OTC/Language"

    /** The language the app shows, as every request's lang says it (WSClient.exchange). */
    @Volatile var wireCode: String = Languages.SOURCE
        internal set

    private val _choice = MutableStateFlow(LanguageChoice())
    val choice: StateFlow<LanguageChoice> = _choice

    private val _deviceKeepsIt = MutableStateFlow<Boolean?>(null)
    /** Whether the device keeps the choice for every app: null until it has said, false for one too old to (the Language screen says so). */
    val deviceKeepsIt: StateFlow<Boolean?> = _deviceKeepsIt

    /** unknown_payload on this connection: nothing is sent until the next, and what was chosen stays pending. */
    @Volatile private var tooOld = false
    private val lock = Any()
    private val sendLock = Mutex()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    /** The picker is offered only while the build has more than one language: English alone has nothing to choose. */
    val pickerShown: Boolean get() = Languages.all.size > 1

    private fun prefs(ctx: Context): SharedPreferences = ctx.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /**
     * Application.onCreate: the copy, and below API 33 the language handed
     * to AppCompat again (it only keeps it in memory; from 33 the system
     * keeps it). Only sets a static there: the first Activity applies it.
     */
    fun start(ctx: Context) {
        _choice.value = load(ctx)
        if (Build.VERSION.SDK_INT < 33) {
            val tag = LanguageChoice.tagFor(_choice.value.language)
            AppCompatDelegate.setApplicationLocales(LocaleListCompat.forLanguageTags(tag))
            saveApplied(ctx, tag)
        }
        refreshWire(ctx, notify = false)
    }

    /**
     * MainActivity's start (after super.onCreate, so AppCompat has its
     * delegate): a language picked in the system's per-app screen becomes a
     * change made on this phone, and a copy the screen doesn't show yet -
     * a change that came from the device - is applied (one recreation).
     */
    fun atActivityStart(activity: Activity) {
        val sdk = Build.VERSION.SDK_INT
        val p = prefs(activity)
        val platform = platformTags(activity)
        val applied = if (p.contains(KEY_APPLIED)) p.getString(KEY_APPLIED, "") else null
        val appliedSdk = p.getInt(KEY_APPLIED_SDK, 0)
        if (sdk >= 33 && platform != applied) {
            if (applied != null && appliedSdk == sdk) {
                // Picked in Settings > Apps > Languages: this phone's change,
                // shown already, and sent to the device.
                saveApplied(activity, platform)
                choose(LanguageChoice.codeFor(platform))
                refreshWire(activity, notify = true)
                return
            }
            if (applied == null && platform.isNotEmpty()) {
                // A per-app language this app never set: restored with the
                // phone, or picked before the app first ran. The phone's
                // language until the device says, not sent to it.
                saveApplied(activity, platform)
                update { it.copy(language = LanguageChoice.codeFor(platform)) }
                refreshWire(activity, notify = true)
                return
            }
            // Recorded on another API level (an OS upgrade): what the system
            // reports now says nothing about a choice.
        }
        val want = LanguageChoice.tagFor(_choice.value.language)
        if (!LanguageChoice.showsAlready(platform, want)) {
            saveApplied(activity, want)
            AppCompatDelegate.setApplicationLocales(LocaleListCompat.forLanguageTags(want))
            refreshWire(activity, notify = true, tags = want)
        } else {
            if (applied != platform || appliedSdk != sdk) saveApplied(activity, platform)
            refreshWire(activity, notify = true)
        }
    }

    /** Picked on the Language screen: shown at once (the Activity is recreated) and sent to the device. */
    fun choose(context: Context, code: String) {
        val tag = LanguageChoice.tagFor(code)
        saveApplied(context, tag)
        AppCompatDelegate.setApplicationLocales(LocaleListCompat.forLanguageTags(tag))
        refreshWire(context, notify = true, tags = tag)
        choose(code)
    }

    /**
     * The Language screen, open while the copy changed (the device answered
     * "changed", or another app chose meanwhile): applied at once - there is
     * no form to lose there.
     */
    fun applyFromLanguageScreen(context: Context) {
        val want = LanguageChoice.tagFor(_choice.value.language)
        if (LanguageChoice.showsAlready(platformTags(context), want)) return
        saveApplied(context, want)
        AppCompatDelegate.setApplicationLocales(LocaleListCompat.forLanguageTags(want))
        refreshWire(context, notify = true, tags = want)
    }

    private fun choose(code: String) {
        update { it.chosen(code, System.currentTimeMillis()) }
        sendPending()
    }

    fun deviceSaid(status: Status) = deviceSaid(if (status.hasLanguage()) status.language else null)

    fun deviceSaid(settings: Settings) = deviceSaid(if (settings.hasLanguage()) settings.language else null)

    /**
     * The device's value (null: a device that predates it), from a Status
     * poll or a Settings fetch: kept as the copy, and shown from
     * MainActivity's next start.
     */
    fun deviceSaid(value: String?) {
        if (value == null) {
            _deviceKeepsIt.value = false
            return
        }
        _deviceKeepsIt.value = true
        update { it.deviceSaid(value, System.currentTimeMillis()) }
    }

    /** Signed in (again): a device that was too old may have been updated, and a change that didn't reach it goes now. */
    fun connected() {
        tooOld = false
        sendPending()
    }

    /** Log Out: the copy stays, and with it the language shown. */
    fun loggedOut() {
        update { it.loggedOut() }
        tooOld = false
        _deviceKeepsIt.value = null
    }

    /** A configuration change reached the Application: a new system language (Automatic) or per-app one. */
    fun configurationChanged(ctx: Context) {
        refreshWire(ctx, notify = true)
    }

    /**
     * A context whose resources speak the app's language, for text built
     * outside an Activity (the notification channel): below API 33 the
     * Application context never follows the per-app language.
     */
    fun localizedContext(ctx: Context): Context {
        val tags = platformTags(ctx)
        val locales = LocaleList.forLanguageTags(tags.ifEmpty { systemLocales(ctx).toLanguageTags() })
        val config = Configuration(ctx.resources.configuration).apply { setLocales(locales) }
        return ctx.createConfigurationContext(config)
    }

    /** The system's language among this build's: what Automatic shows. */
    fun systemLanguage(ctx: Context): String = LanguageChoice.matchSystem(systemList(ctx))

    /** The row the Language screen marks: "" for Automatic; a code this build doesn't have marks English, which is what it shows. */
    fun selected(language: String): String = if (language.isEmpty()) "" else LanguageChoice.effective(language, emptyList())

    /** A language's own name ("Español"); English's for a code this build doesn't have. */
    fun nameOf(code: String): String =
        (Languages.forCode(code) ?: Languages.forCode(Languages.SOURCE))?.name ?: code

    /** The Settings row's value: "Automatic", or the language's own name. */
    fun currentName(language: String): UiText =
        if (language.isEmpty()) S.appSettingsLanguageAutomatic() else UiText.Raw(nameOf(selected(language)))

    /** The app's per-app language as Android has it: "" for the system's. */
    private fun platformTags(ctx: Context): String =
        if (Build.VERSION.SDK_INT >= 33) LocaleManagerCompat.getApplicationLocales(ctx).toLanguageTags()
        // Below 33 only AppCompat knows it (what start() handed it).
        else AppCompatDelegate.getApplicationLocales().toLanguageTags()

    /** The system's languages - never the app's (from 33 every other API reports the app's). */
    private fun systemLocales(ctx: Context): LocaleListCompat = LocaleManagerCompat.getSystemLocales(ctx)

    private fun systemList(ctx: Context): List<Locale> {
        val l = systemLocales(ctx)
        return (0 until l.size()).mapNotNull { l[it] }
    }

    /**
     * The language shown now, for every request's lang; when it changes the
     * notification channel is named again and the push token re-registered,
     * so the device writes this phone's pushes in it.
     */
    private fun refreshWire(ctx: Context, notify: Boolean, tags: String = platformTags(ctx)) {
        val code = if (tags.isEmpty()) LanguageChoice.matchSystem(systemList(ctx)) else LanguageChoice.codeFor(tags).ifEmpty { Languages.SOURCE }
        if (code == wireCode) return
        wireCode = code
        if (!notify) return
        val app = ctx.applicationContext
        FCMPush.createChannel(app)
        if (OTCConnection.authenticated.value) FCMPush.registerKnown(app)
    }

    private fun sendPending() {
        if (tooOld || !_choice.value.pending) return
        // The window counts from now, before the coroutine runs: a Status
        // answered meanwhile (at a connection, the first poll) doesn't undo
        // a change made long ago that is only now being sent.
        update { if (it.pending) it.sending(System.currentTimeMillis()) else it }
        scope.launch { sendLock.withLock { sendLoop() } }
    }

    /** One SetLanguage at a time, again while a newer choice waits (at most three in a row; the rest goes at the next connection). */
    private suspend fun sendLoop() {
        repeat(3) {
            val st = _choice.value
            if (!st.pending || tooOld) return
            val sent = st.language
            val expected = st.seen
            update { it.sending(System.currentTimeMillis()) }
            val answer = try {
                LanguageAnswer.of(
                    OTCConnection.request(timeoutMs = 60_000L) {
                        val m = SetLanguage.newBuilder().setLanguage(sent)
                        if (expected != null) m.expected = expected
                        it.setReqSetLanguage(m)
                    },
                )
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                Log.w(TAG, "could not send the language: ${e.message}")
                LanguageAnswer.FAILED
            }
            when (answer) {
                LanguageAnswer.TAKEN -> {
                    _deviceKeepsIt.value = true
                    update { it.taken(sent) }
                }
                LanguageAnswer.CHANGED -> {
                    val value = deviceValue() ?: return
                    update { it.refused(sent, value) }
                }
                // Still pending: sent again at the next connection.
                LanguageAnswer.TOO_OLD -> {
                    tooOld = true
                    _deviceKeepsIt.value = false
                    return
                }
                // Still pending: sent again at the next connection.
                LanguageAnswer.FAILED -> return
            }
        }
    }

    /** The device's value now (after a "changed"); null when it didn't say. */
    private suspend fun deviceValue(): String? = try {
        val resp = OTCConnection.request(timeoutMs = 60_000L) { it.setReqGetSettings(GetSettings.getDefaultInstance()) }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_SETTINGS && resp.respSettings.hasLanguage()) resp.respSettings.language else null
    } catch (e: CancellationException) {
        throw e
    } catch (e: Exception) {
        null
    }

    private fun update(f: (LanguageChoice) -> LanguageChoice) {
        synchronized(lock) {
            val before = _choice.value
            val after = f(before)
            if (after == before) return
            _choice.value = after
            save(after)
        }
    }

    private fun load(ctx: Context): LanguageChoice {
        val p = prefs(ctx)
        return LanguageChoice(
            language = p.getString(KEY_LANGUAGE, "") ?: "",
            pending = p.getBoolean(KEY_PENDING, false),
            pendingAt = p.getLong(KEY_PENDING_AT, 0L),
            seen = if (p.contains(KEY_SEEN)) p.getString(KEY_SEEN, null) else null,
        )
    }

    private fun save(c: LanguageChoice) {
        prefs(cloud.offthe.otc.OTCApp.instance).edit {
            putString(KEY_LANGUAGE, c.language)
            putBoolean(KEY_PENDING, c.pending)
            putLong(KEY_PENDING_AT, c.pendingAt)
            if (c.seen != null) putString(KEY_SEEN, c.seen) else remove(KEY_SEEN)
        }
    }

    private fun saveApplied(ctx: Context, tags: String) {
        prefs(ctx).edit { putString(KEY_APPLIED, tags).putInt(KEY_APPLIED_SDK, Build.VERSION.SDK_INT) }
    }
}
