// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  NotificationsBellView.swift
//  OffTheCloud
//
//  Issue #78: originally a bell button + sheet; issue #78 follow-up made
//  notifications a section of its own (leftmost tab, per the user's ask),
//  so this is now that tab's full-page content instead.
//

import SwiftUI

struct NotificationsListView: View {
    @ObservedObject var model: NotificationsModel

    private static let relativeFormatter: RelativeDateTimeFormatter = {
        let f = RelativeDateTimeFormatter()
        f.unitsStyle = .abbreviated
        return f
    }()

    private static func describe(_ n: Msg_Notification) -> String {
        switch n.type {
        case .notificationLikePublication: return "liked your post"
        case .notificationLikeComment: return "liked your comment"
        case .notificationNewComment: return "commented on your post"
        case .notificationFriendRequest: return "sent you a friend request"
        case .notificationFriendAccepted: return "accepted your friend request"
        case .UNRECOGNIZED: return ""
        }
    }

    var body: some View {
        NavigationView {
            Group {
                if model.loadingList && model.notifications.isEmpty {
                    ProgressView()
                } else if model.notifications.isEmpty {
                    Text("Nothing yet").foregroundStyle(.secondary)
                } else {
                    List(model.notifications, id: \.uuid) { n in
                        Button {
                            model.handleTap(n)
                        } label: {
                            HStack(spacing: 12) {
                                // Issue #78 follow-up: who did this, at the
                                // left - same avatar convention as the
                                // Social feed/likers list.
                                avatarView(data: n.hasActorImage ? n.actorImage : nil, size: 40)
                                VStack(alignment: .leading, spacing: 2) {
                                    (Text(n.actorName.isEmpty ? n.actorDomain : n.actorName).bold()
                                        + Text(" " + Self.describe(n)))
                                        .foregroundStyle(.primary)
                                        .font(.subheadline)
                                    if n.hasDt {
                                        Text(Self.relativeFormatter.localizedString(for: n.dt.date, relativeTo: Date()))
                                            .font(.caption2)
                                            .foregroundStyle(.secondary)
                                    }
                                }
                                Spacer()
                                // What it's about, at the right - unset for
                                // FriendRequest/FriendAccepted, which point
                                // at no post.
                                if n.hasThumbnail, let ui = UIImage(data: n.thumbnail) {
                                    Image(uiImage: ui)
                                        .resizable()
                                        .aspectRatio(contentMode: .fill)
                                        .frame(width: 44, height: 44)
                                        .clipShape(RoundedRectangle(cornerRadius: 6))
                                }
                            }
                            // Still-unread-as-of-this-open rows read
                            // slightly highlighted - a fading distinction,
                            // not persistent, since opening this tab marks
                            // everything read a moment after it loads.
                            .listRowBackground(n.acknowledged ? Color.clear : Color.yellow.opacity(0.08))
                        }
                        .buttonStyle(.plain)
                    }
                    .listStyle(.plain)
                    .refreshable { await model.openPanel() }
                }
            }
            .navigationTitle("Notifications")
        }
        .navigationViewStyle(.stack)
        .task { await model.openPanel() }
    }
}
