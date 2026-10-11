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

    /// When it happened, in the app's language: made when shown (the
    /// formatter is cheap, and a kept one would stay in the language it was
    /// made in).
    private static func relativeTime(_ date: Date) -> String {
        let f = RelativeDateTimeFormatter()
        f.unitsStyle = .abbreviated
        f.locale = L10n.shared.locale
        return f.localizedString(for: date, relativeTo: Date())
    }

    /// Who did it (in bold) and what, as one sentence per kind; nil for a
    /// kind this app doesn't know, which shows only who.
    private static func describe(_ n: Msg_Notification) -> AttributedString? {
        let name = n.actorName.isEmpty ? n.actorDomain : n.actorName
        switch n.type {
        case .notificationLikePublication: return S.appSocialAlertsLikedPost(name: name, b: bold)
        case .notificationLikeComment: return S.appSocialAlertsLikedComment(name: name, b: bold)
        case .notificationNewComment: return S.appSocialAlertsCommented(name: name, b: bold)
        case .notificationFriendRequest: return S.appSocialAlertsFriendRequest(name: name, b: bold)
        case .notificationFriendAccepted: return S.appSocialAlertsFriendAccepted(name: name, b: bold)
        case .notificationError, .notificationUpdate, .UNRECOGNIZED: return nil
        }
    }

    private static var bold: AttributeContainer {
        var b = AttributeContainer()
        b.inlinePresentationIntent = .stronglyEmphasized
        return b
    }

    /// The "+N more" of a group of errors, in grey after its title.
    private static var dim: AttributeContainer {
        var d = AttributeContainer()
        d.foregroundColor = .secondary
        return d
    }

    // Issue #64: which error rows show their full list of errors - a tap
    // toggles it, the phone's counterpart of the web's hover.
    @State private var expandedErrors: Set<String> = []
    @Environment(\.wideLayout) private var wide

    var body: some View {
        NavigationStack {
            Group {
                if model.loadingList && model.notifications.isEmpty {
                    ProgressView()
                } else if model.notifications.isEmpty {
                    Text(S.appSocialAlertsEmpty).foregroundStyle(.secondary)
                } else {
                    List(model.notifications, id: \.uuid) { n in
                        if n.type == .notificationError {
                            errorRow(n)
                        } else if n.type == .notificationUpdate {
                            updateRow(n)
                        } else {
                        HStack(spacing: 8) {
                        Button {
                            model.handleTap(n)
                        } label: {
                            HStack(spacing: 12) {
                                // Issue #78 follow-up: who did this, at the
                                // left - same avatar convention as the
                                // Social feed/likers list.
                                avatarView(data: n.hasActorImage ? n.actorImage : nil, size: 40)
                                VStack(alignment: .leading, spacing: 2) {
                                    Group {
                                        if let line = Self.describe(n) {
                                            Text(line)
                                        } else {
                                            Text(n.actorName.isEmpty ? n.actorDomain : n.actorName).bold()
                                        }
                                    }
                                        .foregroundStyle(.primary)
                                        .font(.subheadline)
                                    if n.hasDt {
                                        Text(Self.relativeTime(n.dt.date))
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
                        }
                        .buttonStyle(.plain)
                        if n.type == .notificationFriendRequest {
                            if model.awaitingAnswer.contains(n.actorDomain) {
                                Button(S.commonAccept) { Task { await model.accept(domain: n.actorDomain) } }
                                    .buttonStyle(.borderedProminent)
                                    .controlSize(.small)
                            } else if model.acceptedHere.contains(n.actorDomain) {
                                Label(S.iosAlertsFriends, systemImage: "checkmark").font(.caption).foregroundStyle(.secondary)
                            }
                        }
                        }
                        .listRowBackground(n.acknowledged ? Color.clear : Color.yellow.opacity(0.08))
                        }
                    }
                    .listStyle(.plain)
                    .refreshable { await model.openPanel() }
                }
            }
            .navigationTitle(S.appSocialAlertsTitle)
            // The wide layout's menu names the page, as the web's does:
            // no title bar there (AppMenu.swift).
            .toolbar(wide ? .hidden : .automatic, for: .navigationBar)
            .overlay(alignment: .topLeading) {
                if wide { HiddenPageHeading(title: S.appShellSectionAlerts) }
            }
        }
        .task { await model.openPanel() }
    }

    /// Issue #183: a device update worth knowing about - which one in the
    /// title, what it changes in the details. A tap opens Settings at its
    /// update section.
    @ViewBuilder
    private func updateRow(_ n: Msg_Notification) -> some View {
        Button {
            model.handleTap(n)
        } label: {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: "arrow.down.circle.fill")
                    .foregroundStyle(.blue)
                    .font(.title3)
                    .frame(width: 40, height: 40)
                VStack(alignment: .leading, spacing: 2) {
                    Text(n.title).bold()
                        .foregroundStyle(.primary)
                        .font(.subheadline)
                    if n.hasDt {
                        Text(Self.relativeTime(n.dt.date))
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                    if !n.details.isEmpty {
                        Text(n.details)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .padding(.top, 4)
                    }
                }
                Spacer()
                Image(systemName: "chevron.right")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .buttonStyle(.plain)
        .listRowBackground(n.acknowledged ? Color.clear : Color.yellow.opacity(0.08))
    }

    /// Issue #64: a device error, or a group of them. One line (the first
    /// error's) plus "+N more"; a tap shows every error's line.
    @ViewBuilder
    private func errorRow(_ n: Msg_Notification) -> some View {
        let expanded = expandedErrors.contains(n.uuid)
        Button {
            if expanded { expandedErrors.remove(n.uuid) } else { expandedErrors.insert(n.uuid) }
        } label: {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
                    .font(.title3)
                    .frame(width: 40, height: 40)
                VStack(alignment: .leading, spacing: 2) {
                    // The title is the device's, already in the app's language.
                    Group {
                        if n.occurrences > 1 {
                            Text(S.appSocialAlertsErrorMore(title: n.title, count: Int(n.occurrences - 1), b: Self.bold, dim: Self.dim))
                        } else {
                            Text(n.title).bold()
                        }
                    }
                        .foregroundStyle(.primary)
                        .font(.subheadline)
                    if n.hasDt {
                        Text(Self.relativeTime(n.dt.date))
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                    if expanded, !n.details.isEmpty {
                        Text(n.details)
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                            .textSelection(.enabled)
                            .padding(.top, 4)
                    }
                }
                Spacer()
                Image(systemName: expanded ? "chevron.up" : "chevron.down")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .listRowBackground(n.acknowledged ? Color.clear : Color.yellow.opacity(0.08))
        }
        .buttonStyle(.plain)
        .accessibilityLabel(expanded
            ? S.iosAlertsErrorsCollapse(title: n.title, count: Int(n.occurrences))
            : S.iosAlertsErrorsExpand(title: n.title, count: Int(n.occurrences)))
    }
}
