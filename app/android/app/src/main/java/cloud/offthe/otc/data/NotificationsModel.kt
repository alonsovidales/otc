// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import cloud.offthe.otc.i18n.S
import cloud.offthe.otc.i18n.UiText
import cloud.offthe.otc.i18n.appSocialAlertsAcceptFailed
import cloud.offthe.otc.net.NetworkWatch
import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.net.retryWaitMs
import cloud.offthe.otc.net.sleepOrWake
import cloud.offthe.otc.proto.ReqEnvelope
import cloud.offthe.otc.proto.ReqGetNotificationCount
import cloud.offthe.otc.proto.ReqMarkNotificationsAcknowledged
import cloud.offthe.otc.proto.Notification
import cloud.offthe.otc.proto.NotificationType
import cloud.offthe.otc.proto.ReqListNotifications
import cloud.offthe.otc.proto.RespEnvelope
import cloud.offthe.otc.ui.common.LoadProblem
import cloud.offthe.otc.ui.common.backOnline
import cloud.offthe.otc.ui.common.loadProblem
import cloud.offthe.otc.ui.common.now
import cloud.offthe.otc.ui.social.ackOk
import cloud.offthe.otc.ui.social.refusal
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull

// Port of NotificationsModel.swift (issue #78): the bell's unread count -
// asked for when the app comes to the front, when a push arrives and every
// 45 s otherwise (CountPoll) - and the list.
object NotificationsModel {
    sealed class DeepLink {
        data class Post(val pubUuid: String, val commentUuid: String?) : DeepLink()
        object FriendRequests : DeepLink()
    }

    /** The count's wait between asks while the app is in front: a push or a return to the front asks at once. */
    const val POLL_MS = 45_000L

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val _unacknowledgedCount = MutableStateFlow(0)
    private val _notifications = MutableStateFlow<List<Notification>>(emptyList())
    private val _loadingList = MutableStateFlow(false)
    private val _loadProblem = MutableStateFlow<LoadProblem?>(null)
    private val _notice = MutableStateFlow<UiText?>(null)
    private val _pendingDeepLink = MutableStateFlow<DeepLink?>(null)
    private val _alertsRequested = MutableStateFlow(false)
    private val _settingsRequested = MutableStateFlow(false)
    private val _awaitingAnswer = MutableStateFlow<Set<String>>(emptySet())
    private val _acceptedHere = MutableStateFlow<Set<String>>(emptySet())
    /** Domains whose friend request is still waiting: their alert gets an Accept button. */
    val awaitingAnswer: StateFlow<Set<String>> = _awaitingAnswer
    /** Accepted from Alerts during this open, shown as "Friends". */
    val acceptedHere: StateFlow<Set<String>> = _acceptedHere
    val unacknowledgedCount: StateFlow<Int> = _unacknowledgedCount
    val notifications: StateFlow<List<Notification>> = _notifications
    val loadingList: StateFlow<Boolean> = _loadingList
    /** The last load of the list failed, and why (null: it didn't) - said in place of "Nothing yet", with Try again. */
    val loadProblem: StateFlow<LoadProblem?> = _loadProblem
    /** A short message at the top of Alerts (an Accept the device didn't take), worded when shown. */
    val notice: StateFlow<UiText?> = _notice
    val pendingDeepLink: StateFlow<DeepLink?> = _pendingDeepLink
    /** Issue #125: a tapped push asks MainView to show the Alerts tab. */
    val alertsRequested: StateFlow<Boolean> = _alertsRequested
    /** Issue #183: a tapped update alert asks MainView to show Settings. */
    val settingsRequested: StateFlow<Boolean> = _settingsRequested

    /** Sends Alerts' requests. */
    private val send: suspend ((ReqEnvelope.Builder) -> Unit) -> RespEnvelope = { build -> OTCConnection.request(build = build) }

    fun requestAlerts() { _alertsRequested.value = true }

    /**
     * A tapped push: the same place tapping it in Alerts opens - its post
     * (and comment), or the friend requests. The keys are the bridge's
     * (push.Target); a push without them opens Alerts.
     */
    fun handlePushTap(kind: String?, pubUuid: String?, commentUuid: String?) {
        when {
            kind == "friends" -> _pendingDeepLink.value = DeepLink.FriendRequests
            !pubUuid.isNullOrEmpty() -> _pendingDeepLink.value = DeepLink.Post(pubUuid, commentUuid?.ifEmpty { null })
            else -> requestAlerts()
        }
    }
    fun consumeAlertsRequest() { _alertsRequested.value = false }
    fun consumeSettingsRequest() { _settingsRequested.value = false }

    private val poll = CountPoll(scope, POLL_MS) { fetchCount() }

    /** The app came to the front (RootView's ON_RESUME): the count now, then every POLL_MS. */
    fun startPolling() {
        if (poll.running) poll.kick() else poll.start()
    }

    fun stopPolling() = poll.stop()

    /**
     * A push arrived while the app is open (OTCMessagingService): something
     * new on the device - the count at once, and the list too while Alerts
     * shows it, rather than at the next round of the poll.
     */
    fun pushArrived() {
        poll.kick()
        if (panelShown) scope.launch { openPanel() }
    }

    /** Log Out: nothing unread, nothing listed, nothing to jump to. */
    fun reset() {
        stopPolling()
        retryJob?.cancel()
        retryJob = null
        _unacknowledgedCount.value = 0
        _notifications.value = emptyList()
        _loadingList.value = false
        _loadProblem.value = null
        _notice.value = null
        _pendingDeepLink.value = null
    }

    private suspend fun fetchCount() {
        val resp = try {
            send { it.setReqGetNotificationCount(ReqGetNotificationCount.getDefaultInstance()) }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) { return }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_NOTIFICATION_COUNT) {
            _unacknowledgedCount.value = resp.respNotificationCount.unacknowledgedCount
        }
    }

    // Alerts is on screen (NotificationsListView): a list that failed is
    // asked for again, 1 s doubling to 10 s and at once on a Wake, while it is.
    @Volatile private var panelShown = false
    private var retryJob: Job? = null
    private var failures = 0

    /** Alerts appeared ([shown]) or went: a list that failed is asked for again only while it shows. */
    fun panelShown(shown: Boolean) {
        panelShown = shown
        if (!shown) { retryJob?.cancel(); retryJob = null }
    }

    /** Fetch the list, then mark everything read and zero the badge. */
    suspend fun openPanel() {
        _loadingList.value = true
        _loadProblem.update { it?.backOnline(NetworkWatch.online()) }
        try {
            var resp: RespEnvelope? = null
            val problem: LoadProblem? = try {
                resp = send { it.setReqListNotifications(ReqListNotifications.newBuilder().setLimit(50)) }
                if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_NOTIFICATIONS) null else problemOf(resp, null)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                problemOf(resp, e)
            }
            _loadProblem.value = problem
            if (problem != null) { listFailed(); return }
            failures = 0
            _notifications.value = resp!!.respNotifications.notificationsList
            loadAwaitingAnswer()
            _unacknowledgedCount.value = 0
            cloud.offthe.otc.push.FCMPush.clearShown(cloud.offthe.otc.OTCApp.instance)
            try {
                send { it.setReqMarkNotificationsAcknowledged(ReqMarkNotificationsAcknowledged.getDefaultInstance()) }
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {}
        } finally {
            _loadingList.value = false
        }
    }

    /** The list's Try again: asked for now, the backoff from the start. */
    fun retryList() {
        failures = 0
        retryJob?.cancel()
        retryJob = scope.launch { openPanel() }
    }

    private fun listFailed() {
        if (!panelShown) return
        val wait = retryWaitMs(++failures)
        retryJob?.cancel()
        retryJob = scope.launch {
            sleepOrWake(wait)
            if (panelShown) openPanel()
        }
    }

    private fun problemOf(resp: RespEnvelope?, error: Throwable?) =
        loadProblem(resp, error, NetworkWatch.online(), OTCConnection.statusCode.value)

    private suspend fun loadAwaitingAnswer() {
        if (_notifications.value.none { it.type == NotificationType.NotificationFriendRequest }) return
        val resp = try {
            send { it.setReqFriendshipsList(cloud.offthe.otc.proto.FriendshipsList.getDefaultInstance()) }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) { return }
        if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_FRIENDSHIPS) return
        _awaitingAnswer.value = resp.respFriendships.friendshipsList
            .filter { it.status == cloud.offthe.otc.proto.FriendShipStatus.Pending && !it.sent }
            .map { it.originProfile.domain }.toSet()
    }

    /** Accept, straight from a friend-request alert; one the device didn't take says why (it used to do nothing). */
    suspend fun accept(domain: String) {
        val failure = acceptRefusal(send, domain)
        if (failure != null) { say(failure); return }
        _awaitingAnswer.value = _awaitingAnswer.value - domain
        _acceptedHere.value = _acceptedHere.value + domain
    }

    /** [m] at the top of Alerts for a few seconds, longer for a long sentence (the device's words). */
    private fun say(m: UiText) {
        _notice.value = m
        val length = runCatching { m.now().length }.getOrDefault(0)
        scope.launch {
            delay((2500L + 50L * (length - 50).coerceAtLeast(0)).coerceAtMost(8000L))
            _notice.update { if (it == m) null else it }
        }
    }

    fun handleTap(n: Notification) {
        if (n.type == NotificationType.NotificationUpdate) { _settingsRequested.value = true; return }
        _pendingDeepLink.value = when (n.type) {
            NotificationType.NotificationFriendRequest, NotificationType.NotificationFriendAccepted -> DeepLink.FriendRequests
            else -> if (n.pubUuid.isEmpty()) return else DeepLink.Post(n.pubUuid, n.commentUuid.ifEmpty { null })
        }
    }

    fun consumeDeepLink() { _pendingDeepLink.value = null }
}

/** What the device said to an Accept from Alerts: null once it took it, else why not - its words, or ours. */
internal suspend fun acceptRefusal(send: suspend ((ReqEnvelope.Builder) -> Unit) -> RespEnvelope, domain: String): UiText? = try {
    val resp = send {
        it.setReqChangeFriendStatus(cloud.offthe.otc.proto.ChangeFriendStatus.newBuilder().setDomain(domain).setStatus(cloud.offthe.otc.proto.FriendShipStatus.Accepted))
    }
    if (resp.ackOk()) null else refusal(resp) ?: S.appSocialAlertsAcceptFailed()
} catch (e: CancellationException) {
    throw e
} catch (e: Exception) {
    S.appSocialAlertsAcceptFailed()
}

/** What Alerts shows in place of its list. */
enum class AlertsBody { LOADING, PROBLEM, EMPTY, LIST }

/**
 * A list that failed to load says why, with Try again - it used to read
 * "Nothing yet" as if there were none. A list already shown stays while a
 * refresh of it fails.
 */
fun alertsBody(listEmpty: Boolean, loading: Boolean, problem: LoadProblem?): AlertsBody = when {
    !listEmpty -> AlertsBody.LIST
    problem != null -> AlertsBody.PROBLEM
    loading -> AlertsBody.LOADING
    else -> AlertsBody.EMPTY
}

/**
 * A count asked for at once and then every [waitMs] while started, and at
 * once again when kicked (a push) - as CountPoll in NotificationsModel.swift.
 * The unread count used to be asked for every 5 s whether or not anything
 * could have changed.
 */
class CountPoll(private val scope: CoroutineScope, private val waitMs: Long, private val fetch: suspend () -> Unit) {
    private val kicks = MutableStateFlow(0)
    private var job: Job? = null

    val running: Boolean get() = job?.isActive == true

    /** Started (the app came to the front): asked for now, then every waitMs. A no-op while running. */
    @Synchronized fun start() {
        if (job?.isActive == true) return
        job = scope.launch {
            while (isActive) {
                val seen = kicks.value
                fetch()
                // The wait, cut short by a kick that came during it (or the fetch).
                withTimeoutOrNull(waitMs) { kicks.first { it != seen } }
            }
        }
    }

    @Synchronized fun stop() {
        job?.cancel()
        job = null
    }

    /** Ask now, not at the end of the wait (a no-op while stopped: the next start asks anyway). */
    fun kick() = kicks.update { it + 1 }
}
