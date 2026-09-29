// SPDX-License-Identifier: AGPL-3.0-or-later
package cloud.offthe.otc.data

import cloud.offthe.otc.net.OTCConnection
import cloud.offthe.otc.proto.ReqGetNotificationCount
import cloud.offthe.otc.proto.ReqMarkNotificationsAcknowledged
import cloud.offthe.otc.proto.Notification
import cloud.offthe.otc.proto.NotificationType
import cloud.offthe.otc.proto.ReqListNotifications
import cloud.offthe.otc.proto.RespEnvelope
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

// Port of NotificationsModel.swift (issue #78): the bell's unread count,
// polled every 5s while the app is in the foreground, and the list.
object NotificationsModel {
    sealed class DeepLink {
        data class Post(val pubUuid: String, val commentUuid: String?) : DeepLink()
        object FriendRequests : DeepLink()
    }

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val _unacknowledgedCount = MutableStateFlow(0)
    private val _notifications = MutableStateFlow<List<Notification>>(emptyList())
    private val _loadingList = MutableStateFlow(false)
    private val _pendingDeepLink = MutableStateFlow<DeepLink?>(null)
    private val _alertsRequested = MutableStateFlow(false)
    private val _awaitingAnswer = MutableStateFlow<Set<String>>(emptySet())
    private val _acceptedHere = MutableStateFlow<Set<String>>(emptySet())
    /** Domains whose friend request is still waiting: their alert gets an Accept button. */
    val awaitingAnswer: StateFlow<Set<String>> = _awaitingAnswer
    /** Accepted from Alerts during this open, shown as "Friends". */
    val acceptedHere: StateFlow<Set<String>> = _acceptedHere
    val unacknowledgedCount: StateFlow<Int> = _unacknowledgedCount
    val notifications: StateFlow<List<Notification>> = _notifications
    val loadingList: StateFlow<Boolean> = _loadingList
    val pendingDeepLink: StateFlow<DeepLink?> = _pendingDeepLink
    /** Issue #125: a tapped push asks MainView to show the Alerts tab. */
    val alertsRequested: StateFlow<Boolean> = _alertsRequested

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

    private var pollJob: Job? = null

    fun startPolling() {
        if (pollJob != null) return
        pollJob = scope.launch {
            while (isActive) {
                fetchCount()
                delay(5_000)
            }
        }
    }

    fun stopPolling() {
        pollJob?.cancel()
        pollJob = null
    }

    /** Log Out: nothing unread, nothing listed, nothing to jump to. */
    fun reset() {
        stopPolling()
        _unacknowledgedCount.value = 0
        _notifications.value = emptyList()
        _loadingList.value = false
        _pendingDeepLink.value = null
    }

    private suspend fun fetchCount() {
        val resp = try {
            OTCConnection.request { it.setReqGetNotificationCount(ReqGetNotificationCount.getDefaultInstance()) }
        } catch (e: Exception) { return }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_NOTIFICATION_COUNT) {
            _unacknowledgedCount.value = resp.respNotificationCount.unacknowledgedCount
        }
    }

    /** Fetch the list, then mark everything read and zero the badge. */
    suspend fun openPanel() {
        _loadingList.value = true
        try {
            val resp = try {
                OTCConnection.request { it.setReqListNotifications(ReqListNotifications.newBuilder().setLimit(50)) }
            } catch (e: Exception) { return }
            if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_NOTIFICATIONS) {
                _notifications.value = resp.respNotifications.notificationsList
            }
            loadAwaitingAnswer()
            _unacknowledgedCount.value = 0
            cloud.offthe.otc.push.FCMPush.clearShown(cloud.offthe.otc.OTCApp.instance)
            try {
                OTCConnection.request { it.setReqMarkNotificationsAcknowledged(ReqMarkNotificationsAcknowledged.getDefaultInstance()) }
            } catch (_: Exception) {}
        } finally {
            _loadingList.value = false
        }
    }

    private suspend fun loadAwaitingAnswer() {
        if (_notifications.value.none { it.type == NotificationType.NotificationFriendRequest }) return
        val resp = try {
            OTCConnection.request { it.setReqFriendshipsList(cloud.offthe.otc.proto.FriendshipsList.getDefaultInstance()) }
        } catch (e: Exception) { return }
        if (resp.payloadCase != RespEnvelope.PayloadCase.RESP_FRIENDSHIPS) return
        _awaitingAnswer.value = resp.respFriendships.friendshipsList
            .filter { it.status == cloud.offthe.otc.proto.FriendShipStatus.Pending && !it.sent }
            .map { it.originProfile.domain }.toSet()
    }

    /** Accept, straight from a friend-request alert. */
    suspend fun accept(domain: String) {
        val resp = try {
            OTCConnection.request {
                it.setReqChangeFriendStatus(cloud.offthe.otc.proto.ChangeFriendStatus.newBuilder().setDomain(domain).setStatus(cloud.offthe.otc.proto.FriendShipStatus.Accepted))
            }
        } catch (e: Exception) { return }
        if (resp.payloadCase == RespEnvelope.PayloadCase.RESP_ACK && resp.respAck.ok) {
            _awaitingAnswer.value = _awaitingAnswer.value - domain
            _acceptedHere.value = _acceptedHere.value + domain
        }
    }

    fun handleTap(n: Notification) {
        _pendingDeepLink.value = when (n.type) {
            NotificationType.NotificationFriendRequest, NotificationType.NotificationFriendAccepted -> DeepLink.FriendRequests
            else -> if (n.pubUuid.isEmpty()) return else DeepLink.Post(n.pubUuid, n.commentUuid.ifEmpty { null })
        }
    }

    fun consumeDeepLink() { _pendingDeepLink.value = null }
}
