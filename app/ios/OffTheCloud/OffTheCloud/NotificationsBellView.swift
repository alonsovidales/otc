// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  NotificationsBellView.swift
//  OffTheCloud
//
//  Issue #78: the bell button (top-leading overlay on MainView's ZStack,
//  mirroring how UploadBar is already a top-level overlay there, just
//  anchored opposite) and the sheet it presents.
//

import SwiftUI

struct NotificationsBellButton: View {
    @ObservedObject var model: NotificationsModel
    @State private var sheetOpen = false

    var body: some View {
        Button {
            sheetOpen = true
            Task { await model.openPanel() }
        } label: {
            ZStack(alignment: .topTrailing) {
                Image(systemName: "bell.fill")
                    .font(.system(size: 18))
                    // Issue #78: "if there are unacknowledged notifications
                    // the bell will be highlighted" - full accent color
                    // plus a glow, vs. muted grey at rest, same language
                    // as the web bell.
                    .foregroundStyle(model.unacknowledgedCount > 0 ? Color.yellow : Color.secondary)
                    .shadow(color: model.unacknowledgedCount > 0 ? .yellow.opacity(0.6) : .clear, radius: 6)
                    .padding(8)
                if model.unacknowledgedCount > 0 {
                    Text(model.unacknowledgedCount > 99 ? "99+" : "\(model.unacknowledgedCount)")
                        .font(.system(size: 10, weight: .bold))
                        .foregroundStyle(.white)
                        .padding(.horizontal, 4)
                        .frame(minWidth: 16, minHeight: 16)
                        .background(Color.red, in: Capsule())
                        .offset(x: -2, y: 2)
                }
            }
        }
        .background(.ultraThinMaterial, in: Circle())
        .sheet(isPresented: $sheetOpen) {
            NotificationsSheet(model: model, dismiss: { sheetOpen = false })
        }
    }
}

private struct NotificationsSheet: View {
    @ObservedObject var model: NotificationsModel
    let dismiss: () -> Void

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
                            dismiss()
                        } label: {
                            HStack {
                                VStack(alignment: .leading, spacing: 2) {
                                    (Text(n.actorName.isEmpty ? n.actorDomain : n.actorName).bold()
                                        + Text(" " + Self.describe(n)))
                                        .foregroundStyle(.primary)
                                        .font(.subheadline)
                                }
                                Spacer()
                                if n.hasDt {
                                    Text(Self.relativeFormatter.localizedString(for: n.dt.date, relativeTo: Date()))
                                        .font(.caption2)
                                        .foregroundStyle(.secondary)
                                }
                            }
                            // Still-unread-as-of-this-open rows read
                            // slightly highlighted - a fading distinction,
                            // not persistent, since opening this sheet
                            // marks everything read a moment after it loads.
                            .listRowBackground(n.acknowledged ? Color.clear : Color.yellow.opacity(0.08))
                        }
                        .buttonStyle(.plain)
                    }
                    .listStyle(.plain)
                }
            }
            .navigationTitle("Notifications")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }
}
