// SPDX-License-Identifier: AGPL-3.0-or-later

//
//  OffTheCloudWidget.swift
//  OffTheCloudWidget
//
//  The home screen widget: one of a collection's photos, a different one
//  every hour, from the copies the app stored for it in the App Group's
//  container (Wallpaper.swift stores them; WallpaperShared.swift, compiled
//  into both, has where they are and the order they come in). Its one
//  setting is the collection, among those with copies stored. Small,
//  medium and large; the small one also shows in StandBy. Tapping it opens
//  the app. Its texts are in the language chosen in the app (kept in the
//  App Group), through the app's own catalog (L10n, S+Ios.swift and
//  Ios.xcstrings are compiled in here too).
//

import AppIntents
import ImageIO
import SwiftUI
import UIKit
import WidgetKit

@main
struct OffTheCloudWidgets: WidgetBundle {
    init() {
        WidgetLanguage.apply()
    }

    var body: some Widget {
        CollectionWidget()
    }
}

/// The language chosen in the app, for the widget's texts.
enum WidgetLanguage {
    @MainActor static func apply() {
        guard let code = WallpaperGroup.defaults?.string(forKey: WallpaperGroup.languageKey),
              !code.isEmpty, code != L10n.shared.code else { return }
        L10n.shared.set(code)
    }
}

// MARK: The setting: which collection

/// A collection with copies stored for the widget.
struct CollectionEntity: AppEntity {
    static var typeDisplayRepresentation: TypeDisplayRepresentation {
        TypeDisplayRepresentation(name: LocalizedStringResource("ios.widget_name", table: "Ios"))
    }
    static var defaultQuery = CollectionQuery()

    var id: String
    var name: String

    var displayRepresentation: DisplayRepresentation {
        DisplayRepresentation(title: "\(name)")
    }
}

struct CollectionQuery: EntityQuery {
    /// The collections asked for: stored ones, and one whose copies are
    /// gone by its last name, so the widget says they are gone.
    func entities(for identifiers: [String]) async throws -> [CollectionEntity] {
        let stored = stored()
        let names = WidgetNames.all(WallpaperGroup.defaults)
        return identifiers.compactMap { id in
            stored.first { $0.id == id } ?? names[id].map { CollectionEntity(id: id, name: $0) }
        }
    }

    func suggestedEntities() async throws -> [CollectionEntity] {
        stored()
    }

    /// The collection stored for the widget (one at a time): picked at once
    /// when the widget is added.
    func defaultResult() async -> CollectionEntity? {
        stored().first
    }

    private func stored() -> [CollectionEntity] {
        guard let container = WallpaperGroup.container else { return [] }
        return WidgetCatalog.load(root: WallpaperGroup.widgetRoot(in: container)).collections
            .filter { !$0.files.isEmpty }
            .map { CollectionEntity(id: $0.id, name: $0.name) }
    }
}

struct ChooseCollection: WidgetConfigurationIntent {
    static var title = LocalizedStringResource("ios.widget_name", table: "Ios")
    static var description = IntentDescription(LocalizedStringResource("ios.widget_description", table: "Ios"))

    @Parameter(title: LocalizedStringResource("ios.widget_name", table: "Ios"))
    var collection: CollectionEntity?
}

// MARK: The timeline

struct CollectionEntry: TimelineEntry {
    enum Content: Equatable {
        /// A copy, by its file.
        case photo(URL)
        /// Words instead: no collection chosen, or its copies are gone.
        case message(String)
        /// The widget gallery's preview.
        case placeholder
    }

    let date: Date
    let content: Content
    /// The widget's size, in points: a photo is drawn at exactly its pixels.
    var size: CGSize = .zero
}

struct CollectionProvider: AppIntentTimelineProvider {
    /// One entry an hour, six hours ahead; then a new timeline. Each entry
    /// keeps its photo drawn at the widget's size, and a day of them makes
    /// a timeline WidgetKit refuses as too large.
    static let entries = 6

    func placeholder(in context: Context) -> CollectionEntry {
        CollectionEntry(date: Date(), content: .placeholder)
    }

    func snapshot(for configuration: ChooseCollection, in context: Context) async -> CollectionEntry {
        let files = await photos(configuration)
        guard case .found(let urls) = files, let first = urls.first else {
            return CollectionEntry(date: Date(), content: context.isPreview ? .placeholder : await message(files))
        }
        return CollectionEntry(date: Date(), content: .photo(first), size: context.displaySize)
    }

    func timeline(for configuration: ChooseCollection, in context: Context) async -> Timeline<CollectionEntry> {
        let files = await photos(configuration)
        guard case .found(let urls) = files, let id = configuration.collection?.id else {
            return Timeline(entries: [CollectionEntry(date: Date(), content: await message(files))], policy: .never)
        }
        let byName = Dictionary(urls.map { ($0.lastPathComponent, $0) }, uniquingKeysWith: { a, _ in a })
        let defaults = WallpaperGroup.defaults
        let key = WallpaperShuffle.key(id)
        var rng = SystemRandomNumberGenerator()
        let next = WallpaperShuffle.next(Self.entries, from: urls.map(\.lastPathComponent).sorted(),
                                         shown: defaults?.stringArray(forKey: key) ?? [], using: &rng)
        defaults?.set(next.shown, forKey: key)
        let now = Date()
        let entries = next.picks.enumerated().compactMap { i, name -> CollectionEntry? in
            guard let url = byName[name] else { return nil }
            return CollectionEntry(date: now.addingTimeInterval(TimeInterval(i) * 3600), content: .photo(url), size: context.displaySize)
        }
        return Timeline(entries: entries, policy: .atEnd)
    }

    enum Files {
        case notChosen
        case notStored
        case found([URL])
    }

    /// The chosen collection's copies on this iPhone.
    private func photos(_ configuration: ChooseCollection) async -> Files {
        guard let id = configuration.collection?.id else { return .notChosen }
        guard let container = WallpaperGroup.container else { return .notStored }
        let root = WallpaperGroup.widgetRoot(in: container)
        guard let c = WidgetCatalog.load(root: root).collection(id) else { return .notStored }
        let urls = WidgetCatalog.files(of: c, root: root).filter { FileManager.default.fileExists(atPath: $0.path) }
        return urls.isEmpty ? .notStored : .found(urls)
    }

    /// What the widget says without a photo, in the app's language.
    @MainActor
    private func message(_ files: Files) -> CollectionEntry.Content {
        WidgetLanguage.apply()
        switch files {
        case .notChosen: return .message(S.iosWidgetChoose)
        case .notStored, .found: return .message(S.iosWidgetNotStored)
        }
    }
}

// MARK: The widget

struct CollectionWidget: Widget {
    var body: some WidgetConfiguration {
        AppIntentConfiguration(kind: WallpaperGroup.widgetKind, intent: ChooseCollection.self, provider: CollectionProvider()) { entry in
            CollectionWidgetView(entry: entry)
        }
        .configurationDisplayName(Self.name)
        .description(Self.about)
        .supportedFamilies([.systemSmall, .systemMedium, .systemLarge])
        .contentMarginsDisabled()
    }

    /// In the app's language (the widget gallery).
    @MainActor private static var name: String {
        WidgetLanguage.apply()
        return S.iosWidgetName
    }

    @MainActor private static var about: String {
        WidgetLanguage.apply()
        return S.iosWidgetDescription
    }
}

struct CollectionWidgetView: View {
    let entry: CollectionEntry
    @Environment(\.displayScale) private var scale

    var body: some View {
        // Decoded once for the frame.
        let photo: UIImage? = {
            if case .photo(let url) = entry.content { return Self.image(url, filling: entry.size, scale: scale) }
            return nil
        }()
        Group {
            switch entry.content {
            case .photo:
                if photo == nil { words(S.iosWidgetNotStored) } else { Color.clear }
            case .message(let text):
                words(text)
            case .placeholder:
                Color.clear
            }
        }
        .containerBackground(for: .widget) {
            if let photo {
                Image(uiImage: photo)
                    .resizable()
                    .scaledToFill()
                    .accessibilityHidden(true)
            } else if entry.content == .placeholder {
                LinearGradient(colors: [Color(.systemGray4), Color(.systemGray2)], startPoint: .topLeading, endPoint: .bottomTrailing)
            } else {
                Color(.secondarySystemBackground)
            }
        }
    }

    private func words(_ text: String) -> some View {
        VStack(spacing: 8) {
            Image(systemName: "photo.on.rectangle.angled")
                .font(.title2)
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)
            Text(text)
                .font(.caption)
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
                .minimumScaleFactor(0.8)
        }
        .padding(12)
    }

    /// A copy as the widget shows it: cropped to fill `size` and drawn at
    /// exactly its pixels, in the format WidgetKit keeps (a widget
    /// extension has little memory, and a picture bigger than the widget,
    /// or in another pixel format, makes its timeline fail).
    static func image(_ url: URL, filling size: CGSize, scale: CGFloat) -> UIImage? {
        guard size.width > 0, size.height > 0, let src = CGImageSourceCreateWithURL(url as CFURL, nil),
              let props = CGImageSourceCopyPropertiesAtIndex(src, 0, nil) as? [CFString: Any],
              var w = (props[kCGImagePropertyPixelWidth] as? NSNumber)?.doubleValue,
              var h = (props[kCGImagePropertyPixelHeight] as? NSNumber)?.doubleValue, w > 0, h > 0 else { return nil }
        if let o = (props[kCGImagePropertyOrientation] as? NSNumber)?.intValue, o >= 5 { swap(&w, &h) }
        let px = CGSize(width: (size.width * scale).rounded(), height: (size.height * scale).rounded())
        // Read only as many pixels as filling the widget needs.
        let fill = max(px.width / w, px.height / h)
        let opts: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceThumbnailMaxPixelSize: Int((max(w, h) * min(fill, 1)).rounded(.up)),
        ]
        guard let cg = CGImageSourceCreateThumbnailAtIndex(src, 0, opts as CFDictionary) else { return nil }
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        format.opaque = true
        return UIGraphicsImageRenderer(size: px, format: format).image { _ in
            let iw = CGFloat(cg.width), ih = CGFloat(cg.height)
            let k = max(px.width / iw, px.height / ih)
            let drawn = CGSize(width: iw * k, height: ih * k)
            UIImage(cgImage: cg).draw(in: CGRect(x: (px.width - drawn.width) / 2, y: (px.height - drawn.height) / 2,
                                                 width: drawn.width, height: drawn.height))
        }
    }
}
