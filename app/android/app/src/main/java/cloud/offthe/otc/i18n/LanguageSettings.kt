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
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger

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
//
// A change is sent as SetLanguage once a sign-in is accepted - never
// before: the device answers not_authenticated, and asking would only
// start a sign-in of its own (OTCConnection.request connects first). It
// goes expecting the device value this phone saw when the change was made,
// so a change made meanwhile from another app isn't overwritten: the device
// answers "changed" and that one is adopted.
//
// A change made before any value of a device was seen - no device set up
// (the welcome screen, a fresh install, after Log Out), the session ended
// by the device (a password it turned down) or new credentials saved, not
// signed in since launch, a device that predates the field - is for the
// next device: nothing was seen to compare it with, so it goes without
// `expected` (never "") at the next sign-in the device accepts, which makes
// it that device's, whatever another app set there (a new device has
// Automatic). Log Out and a relaunch keep it; values a device reports
// before that sign-in don't replace it. Leaving a device from the "isn't
// available" and "can't connect" cards carries a change still pending to
// the next device the same way: it never reached the one left.
//
// A change of language recreates the Activity, except while a screen
// follows it in place (LanguageSettings.holdInPlace: the Bluetooth setup,
// whose link and page a recreation would tear down; MainActivity).

/** The choice and what the device's answers do to it, as plain values (LanguageChoice in LanguageSettings.swift). */
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
    /** The device value the pending change was made against, SetLanguage's expected; null when none had been seen (left out, never ""). */
    val expected: String? = null,
    /**
     * The pending change is for the device this phone signs in to next:
     * made before any value of a device was seen, or carried from a device
     * left. Log Out keeps it; the next accepted sign-in ([connected]) makes
     * it that device's.
     */
    val forNextDevice: Boolean = false,
    /**
     * The device's value this phone last saw in this session; null when it
     * has seen none since launch, the last sign-in ended, or Log Out. Kept
     * in memory only.
     */
    val seen: String? = null,
) {
    /**
     * Chosen on this phone: shown at once, and pending until the device
     * takes it - against the value seen, or for the next device when none
     * was (or a change for the next device is still waiting).
     */
    fun chosen(code: String, nowMs: Long): LanguageChoice {
        val next = seen == null || (forNextDevice && pending)
        return copy(language = code, pending = true, pendingAt = nowMs, expected = if (next) null else seen, forNextDevice = next)
    }

    /** Being sent: the window counts from now. */
    fun sending(nowMs: Long) = copy(pendingAt = nowMs)

    /** The device took [sent]. A change made while it was on its way goes next, against [sent]. */
    fun taken(sent: String) = when {
        // pendingAt stays: it starts the window.
        language == sent -> copy(seen = sent, pending = false, expected = null, forNextDevice = false)
        pending -> copy(seen = sent, expected = sent, forNextDevice = false)
        else -> copy(seen = sent)
    }

    /**
     * The device refused [sent] because its value had changed meanwhile (Ack
     * code "changed"), and holds [value]: adopted, unless this phone has
     * chosen again since - that one goes next, expecting [value].
     */
    fun refused(sent: String, value: String) =
        if (language == sent) copy(language = value, seen = value).cleared()
        else copy(seen = value, expected = value, forNextDevice = false)

    /** "changed" with the device's value unread: [sent] is no longer pending, and the device's next Status or Settings value is adopted. */
    fun refused(sent: String) = if (language == sent) copy(seen = null).cleared() else copy(seen = null)

    /**
     * The device's value, from its Status or Settings (null: a device that
     * predates the field): adopted, unless it is an older answer to a change
     * of ours still within [WINDOW_MS], or a change for the next device
     * waits for its sign-in.
     */
    fun deviceSaid(value: String?, nowMs: Long): LanguageChoice {
        if (value == null) return this
        // Waiting for the next device's sign-in: what a device says before it doesn't count.
        if (pending && forNextDevice) return this
        // Ours, seen: nothing left to wait for.
        if (value == language) return copy(seen = value).cleared()
        if (pendingAt != 0L) {
            val age = nowMs - pendingAt
            if (age in 0 until WINDOW_MS) return this
        }
        return copy(language = value, seen = value).cleared()
    }

    /**
     * A sign-in the device accepted: a change for the next device is this
     * device's from now on (sent to it now, and dropped like any other by a
     * later Log Out).
     */
    fun connected() = copy(forNextDevice = false)

    /**
     * The session ended without Log Out (the device turned the password
     * down, or new credentials were saved): the value seen goes with it, so
     * a change made before the next sign-in goes without expected. A change
     * already pending keeps its own.
     */
    fun sessionEnded() = copy(seen = null)

    /**
     * Log Out: the copy stays; what belonged to the device signed in to goes
     * - a change for the next device stays, and so does one still pending
     * when [carryPending] (leaving from the cards: it never reached the
     * device left), as one for the next device.
     */
    fun loggedOut(carryPending: Boolean = false) =
        if (pending && (forNextDevice || carryPending)) copy(seen = null, expected = null, forNextDevice = true)
        else copy(seen = null).cleared()

    private fun cleared() = copy(pending = false, pendingAt = 0L, expected = null, forNextDevice = false)

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

        // The prefs file's keys (LanguageSettings.PREFS).
        internal const val KEY_LANGUAGE = "language"
        internal const val KEY_PENDING = "pending"
        internal const val KEY_PENDING_AT = "pendingAt"
        internal const val KEY_EXPECTED = "expected"
        internal const val KEY_FOR_NEXT_DEVICE = "forNextDevice"
        /** Written by builds before the next-device rule: seen is kept in memory only now. */
        internal const val KEY_SEEN_BEFORE = "seen"

        /**
         * A choice as kept across launches, from the prefs file's values
         * ([toStored]): expected and forNextDevice only while pending, and
         * never seen. A relaunch is no change of device: a pending change
         * stays so.
         */
        fun stored(values: Map<String, *>): LanguageChoice {
            val pending = values[KEY_PENDING] as? Boolean ?: false
            return LanguageChoice(
                language = values[KEY_LANGUAGE] as? String ?: "",
                pending = pending,
                pendingAt = values[KEY_PENDING_AT] as? Long ?: 0L,
                expected = if (pending) values[KEY_EXPECTED] as? String else null,
                forNextDevice = pending && values[KEY_FOR_NEXT_DEVICE] == true,
            )
        }
    }

    /** What is kept of it across launches, read back by [stored]: everything but [seen]. A null value is a key removed. */
    fun toStored(): Map<String, Any?> = mapOf(
        KEY_LANGUAGE to language, KEY_PENDING to pending, KEY_PENDING_AT to pendingAt,
        KEY_EXPECTED to expected, KEY_FOR_NEXT_DEVICE to forNextDevice,
    )
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


/** What the device is told and asked (LanguageSettings.Device on iOS); JVM tests stand in for it. */
interface LanguageDevice {
    /** Whether a sign-in has been accepted and holds: nothing is sent before (the connection would start one of its own). */
    fun signedIn(): Boolean

    /** SetLanguage: [expected] null is left out. Throws when the request fails. */
    suspend fun setLanguage(language: String, expected: String?): RespEnvelope

    /** The device's value now, read from its Settings after a "changed"; null when it didn't say. */
    suspend fun language(): String?
}

/**
 * The rules of [LanguageSettings] that need no Android: the choice, the
 * device's answers to it and the sends (LanguageSettings.swift's class).
 * [save] keeps every change, [log] says why a send failed.
 */
internal class LanguageSync(
    private val device: LanguageDevice,
    private val save: (LanguageChoice) -> Unit,
    private val scope: CoroutineScope,
    private val now: () -> Long = System::currentTimeMillis,
    private val log: (String) -> Unit = {},
) {
    private val _choice = MutableStateFlow(LanguageChoice())
    val choice: StateFlow<LanguageChoice> = _choice

    private val _deviceKeepsIt = MutableStateFlow<Boolean?>(null)
    /** Whether the device keeps the choice for every app: null until it has said, false for one too old to (the Language screen says so). */
    val deviceKeepsIt: StateFlow<Boolean?> = _deviceKeepsIt

    /** unknown_payload on this connection: nothing is sent until the next, and what was chosen stays pending. */
    @Volatile private var tooOld = false
    private val lock = Any()
    // One SetLanguage at a time. A send asked for while one is on its way
    // (a newer choice, a sign-in accepted meanwhile) waits its turn, and
    // goes if something is still pending then: a send that failed on the
    // session that ended goes again on the one that signed in, once.
    private val sendLock = Mutex()

    /** At launch, the copy as kept ([LanguageChoice.stored]). */
    fun restore(c: LanguageChoice) {
        synchronized(lock) { _choice.value = c }
    }

    /** Picked on the Language screen or a screen's language menu: sent now if signed in, else at the next sign-in. */
    fun choose(code: String) {
        update { it.chosen(code, now()) }
        sendPending()
    }

    /**
     * The device's value (null: a device that predates it), from a Status
     * poll or a Settings fetch: adopted, unless it is an older answer to a
     * change of ours still on its way, or a change for the next device waits
     * for its sign-in.
     */
    fun deviceSaid(value: String?) {
        if (value == null) {
            _deviceKeepsIt.value = false
            return
        }
        _deviceKeepsIt.value = true
        update { it.deviceSaid(value, now()) }
    }

    /**
     * A sign-in the device accepted (a password, stored credentials, a
     * reconnect; OTCConnection, before that session's first Status or
     * Settings): a device that was too old may have been updated, and a
     * change that didn't reach it - or one made for the next device - goes
     * now.
     */
    fun connected() {
        tooOld = false
        update { if (it.forNextDevice) it.connected() else it }
        sendPending()
    }

    /** The session ended without Log Out (the device turned the password down, or new credentials were saved): the device value seen goes. */
    fun sessionEnded() = update { it.sessionEnded() }

    /**
     * Log Out: the copy stays. [carryPending]: leaving a device from the
     * cards that say it can't be reached or signed in to - a change still
     * pending goes to the next device.
     */
    fun loggedOut(carryPending: Boolean) {
        update { it.loggedOut(carryPending) }
        tooOld = false
        _deviceKeepsIt.value = null
    }

    fun update(f: (LanguageChoice) -> LanguageChoice) {
        synchronized(lock) {
            val before = _choice.value
            val after = f(before)
            if (after == before) return
            _choice.value = after
            save(after)
        }
    }

    private fun sendPending() {
        if (tooOld || !_choice.value.pending || !device.signedIn()) return
        // The window counts from now, before the coroutine runs: a Status
        // answered meanwhile (at a connection, the first poll) doesn't undo
        // a change made long ago that is only now being sent.
        update { if (it.pending) it.sending(now()) else it }
        scope.launch { sendLock.withLock { sendLoop() } }
    }

    /** Again while a newer choice waits (at most three in a row; the rest goes at the next connection). */
    private suspend fun sendLoop() {
        repeat(3) {
            val st = _choice.value
            if (!st.pending || tooOld || !device.signedIn()) return
            val sent = st.language
            val expected = st.expected
            update { it.sending(now()) }
            val answer = try {
                LanguageAnswer.of(device.setLanguage(sent, expected))
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                log("could not send the language: $e")
                LanguageAnswer.FAILED
            }
            when (answer) {
                LanguageAnswer.TAKEN -> {
                    _deviceKeepsIt.value = true
                    update { it.taken(sent) }
                }
                LanguageAnswer.CHANGED -> {
                    val value = device.language()
                    update { if (value != null) it.refused(sent, value) else it.refused(sent) }
                }
                // Still pending: sent again at the next connection.
                LanguageAnswer.TOO_OLD -> {
                    tooOld = true
                    _deviceKeepsIt.value = false
                    return
                }
                // Still pending: sent again at the next connection (or now,
                // by a send asked for meanwhile by a sign-in).
                LanguageAnswer.FAILED -> return
            }
        }
    }
}

/** The app's choice of language: kept here, shown through AppCompat, told to the device. */
object LanguageSettings {
    /** The prefs file: outside SecretsStore's Log Out wipe and outside backup (data_extraction_rules.xml lists otc_settings.xml only). */
    const val PREFS = "otc_language"
    /** The tags last handed to Android ("" for the system's), and the API level then. */
    private const val KEY_APPLIED = "applied"
    private const val KEY_APPLIED_SDK = "appliedSdk"
    private const val TAG = "OTC/Language"

    private val _wire = MutableStateFlow(Languages.SOURCE)
    /** The language the app shows, as every request's lang says it: what the Bluetooth setup's page follows. */
    val wire: StateFlow<String> = _wire

    /** The language the app shows, as every request's lang says it (WSClient.exchange). */
    var wireCode: String
        get() = _wire.value
        internal set(value) { _wire.value = value }

    /**
     * The app's language as a locale for the numbers the app writes by hand
     * (decimalText, the side menu's percentages): the language's own tag,
     * not the region's, so English stays "45.5" on every phone (iOS's
     * L10n.current.locale).
     */
    val numberLocale: Locale get() = Locale.forLanguageTag(Languages.forCode(wireCode)?.tag ?: Languages.SOURCE)

    private val sync = LanguageSync(LiveDevice, save = ::save, scope = CoroutineScope(SupervisorJob() + Dispatchers.IO), log = { Log.w(TAG, it) })

    val choice: StateFlow<LanguageChoice> get() = sync.choice

    /** Whether the device keeps the choice for every app: null until it has said, false for one too old to (the Language screen says so). */
    val deviceKeepsIt: StateFlow<Boolean?> get() = sync.deviceKeepsIt

    /** The picker is offered only while the build has more than one language: English alone has nothing to choose. */
    val pickerShown: Boolean get() = Languages.all.size > 1

    // Screens open that follow a change of language in place (holdInPlace).
    private val inPlace = AtomicInteger()

    /**
     * Whether a change of language is shown where the app is, without
     * recreating the Activity (MainActivity.onConfigurationChanged).
     */
    val followsInPlace: Boolean get() = inPlace.get() > 0

    /**
     * For as long as a screen is open whose state a recreation of the
     * Activity would tear down - the Bluetooth setup: its link to the
     * device and the wizard's page with what was typed in it - a change of
     * language is shown in place (Compose re-reads the resources, and the
     * page is asked to follow). The returned call ends the hold.
     */
    fun holdInPlace(): () -> Unit {
        inPlace.incrementAndGet()
        val held = AtomicBoolean(true)
        return { if (held.getAndSet(false)) inPlace.decrementAndGet() }
    }

    private fun prefs(ctx: Context): SharedPreferences = ctx.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /**
     * Application.onCreate: the copy (a change still pending stays so: a
     * relaunch is no change of device), and below API 33 the language
     * handed to AppCompat again (it only keeps it in memory; from 33 the
     * system keeps it). Only sets a static there: the first Activity
     * applies it.
     */
    fun start(ctx: Context) {
        sync.restore(LanguageChoice.stored(prefs(ctx).all))
        if (Build.VERSION.SDK_INT < 33) {
            val tag = LanguageChoice.tagFor(choice.value.language)
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
                sync.choose(LanguageChoice.codeFor(platform))
                refreshWire(activity, notify = true)
                return
            }
            if (applied == null && platform.isNotEmpty()) {
                // A per-app language this app never set: restored with the
                // phone, or picked before the app first ran. The phone's
                // language until the device says, not sent to it.
                saveApplied(activity, platform)
                sync.update { it.copy(language = LanguageChoice.codeFor(platform)) }
                refreshWire(activity, notify = true)
                return
            }
            // Recorded on another API level (an OS upgrade): what the system
            // reports now says nothing about a choice.
        }
        val want = LanguageChoice.tagFor(choice.value.language)
        if (!LanguageChoice.showsAlready(platform, want)) {
            saveApplied(activity, want)
            AppCompatDelegate.setApplicationLocales(LocaleListCompat.forLanguageTags(want))
            refreshWire(activity, notify = true, tags = want)
        } else {
            if (applied != platform || appliedSdk != sdk) saveApplied(activity, platform)
            refreshWire(activity, notify = true)
        }
    }

    /**
     * Picked on the Language screen or a screen's language menu
     * (LanguageMenu): shown at once (the Activity is recreated, or the
     * screen follows in place) and sent to the device now if signed in,
     * else at the next sign-in.
     */
    fun choose(context: Context, code: String) {
        val tag = LanguageChoice.tagFor(code)
        saveApplied(context, tag)
        AppCompatDelegate.setApplicationLocales(LocaleListCompat.forLanguageTags(tag))
        refreshWire(context, notify = true, tags = tag)
        sync.choose(code)
    }

    /**
     * The Language screen, open while the copy changed (the device answered
     * "changed", or another app chose meanwhile): applied at once - there is
     * no form to lose there.
     */
    fun applyFromLanguageScreen(context: Context) {
        val want = LanguageChoice.tagFor(choice.value.language)
        if (LanguageChoice.showsAlready(platformTags(context), want)) return
        saveApplied(context, want)
        AppCompatDelegate.setApplicationLocales(LocaleListCompat.forLanguageTags(want))
        refreshWire(context, notify = true, tags = want)
    }

    fun deviceSaid(status: Status) = deviceSaid(if (status.hasLanguage()) status.language else null)

    fun deviceSaid(settings: Settings) = deviceSaid(if (settings.hasLanguage()) settings.language else null)

    /**
     * The device's value (null: a device that predates it), from a Status
     * poll or a Settings fetch: kept as the copy, and shown from
     * MainActivity's next start.
     */
    fun deviceSaid(value: String?) = sync.deviceSaid(value)

    /**
     * A sign-in the device accepted (OTCConnection.signedIn, right after
     * authenticated is set, so ahead of that session's first Status or
     * Settings): a change that didn't reach the device, or one made for the
     * next device, goes now.
     */
    fun connected() = sync.connected()

    /** The session ended without Log Out (the device turned the password down, or new credentials were saved): the device value seen goes. */
    fun sessionEnded() = sync.sessionEnded()

    /**
     * Log Out: the copy stays, and with it the language shown.
     * [carryPending]: leaving a device from the cards that say it can't be
     * reached or signed in to - a change still pending goes to the next
     * device.
     */
    fun loggedOut(carryPending: Boolean = false) = sync.loggedOut(carryPending)

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

    /** Through the app's connection. */
    private object LiveDevice : LanguageDevice {
        override fun signedIn(): Boolean = OTCConnection.authenticated.value

        override suspend fun setLanguage(language: String, expected: String?): RespEnvelope =
            OTCConnection.request(timeoutMs = 60_000L) {
                val m = SetLanguage.newBuilder().setLanguage(language)
                if (expected != null) m.expected = expected
                it.setReqSetLanguage(m)
            }

        override suspend fun language(): String? = try {
            val resp = OTCConnection.request(timeoutMs = 60_000L) { it.setReqGetSettings(GetSettings.getDefaultInstance()) }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_SETTINGS && resp.respSettings.hasLanguage()) resp.respSettings.language else null
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            null
        }
    }

    private fun save(c: LanguageChoice) {
        prefs(cloud.offthe.otc.OTCApp.instance).edit {
            for ((key, value) in c.toStored()) {
                when (value) {
                    null -> remove(key)
                    is String -> putString(key, value)
                    is Boolean -> putBoolean(key, value)
                    is Long -> putLong(key, value)
                }
            }
            remove(LanguageChoice.KEY_SEEN_BEFORE)
        }
    }

    private fun saveApplied(ctx: Context, tags: String) {
        prefs(ctx).edit { putString(KEY_APPLIED, tags).putInt(KEY_APPLIED_SDK, Build.VERSION.SDK_INT) }
    }
}
