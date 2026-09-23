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
    val unacknowledgedCount: StateFlow<Int> = _unacknowledgedCount
    val notifications: StateFlow<List<Notification>> = _notifications
    val loadingList: StateFlow<Boolean> = _loadingList
    val pendingDeepLink: StateFlow<DeepLink?> = _pendingDeepLink

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
            _unacknowledgedCount.value = 0
            try {
                OTCConnection.request { it.setReqMarkNotificationsAcknowledged(ReqMarkNotificationsAcknowledged.getDefaultInstance()) }
            } catch (_: Exception) {}
        } finally {
            _loadingList.value = false
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
