// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Observation
import os
import Synchronization // Mutex: iOS 18 / macOS 15

// The app's own text, in the user's language (docs/i18n.md, i18n/README.md).
//
// Every text lives in the catalog under i18n/strings and reaches the code as
// a generated accessor: S.commonCancel, S.appPhotosDeletedBy(name:count:).
// make i18n writes the accessors (S+<Table>.swift), the string catalogs
// they read (<Table>.xcstrings, compiled by Xcode into <lang>.lproj) and
// Languages.swift next to this file; only this file is written by hand.
//
// Lookups go through L10n.shared, an immutable snapshot of the language
// (its code, its locale and its .lproj bundle) behind a Mutex, so any
// thread may call them while the main thread switches language. Reading it
// registers an Observation dependency: a SwiftUI body that shows an
// accessor's text is drawn again when the language changes.
//
// Rules (the rendering contract in docs/i18n.md):
//   - Use the accessors. Never String(localized:), NSLocalizedString,
//     localizedStringWithFormat, Text("literal") for our own text,
//     LocalizedStringKey or AttributedString(markdown:): they pick the
//     system's language or locale, or read text as markup.
//   - Never write AppleLanguages.
//   - Show a String with Text(verbatim:) or Text(someString) (which is
//     verbatim), never through a LocalizedStringKey.

/// The language the app shows its own text in. Thread-safe.
final class L10n: Observable, Sendable {
    static let shared = L10n()

    /// What a lookup reads: one language, all of it resolved at once.
    struct Snapshot: Sendable {
        let language: L10nLanguage
        /// The language's canonical tag: it picks the plural form and formats
        /// the numbers in its texts.
        let locale: Locale
        /// The language's .lproj, or English's when the build has none.
        let bundle: Bundle
        let english: Bundle
    }

    /// Where the .lproj folders are. Tests point it elsewhere once, before
    /// the first lookup.
    nonisolated(unsafe) static var resourceRoot: () -> Bundle = { .main }

    private let registrar = ObservationRegistrar()
    private let state: Mutex<Snapshot>

    // Until the stored choice (phase 1 of docs/i18n.md) the app follows the
    // system language.
    private init() { state = Mutex(L10n.snapshot(for: L10n.systemLanguage())) }

    private static func lproj(_ name: String) -> Bundle? {
        resourceRoot().path(forResource: name, ofType: "lproj").flatMap(Bundle.init(path:))
    }

    private static func snapshot(for code: String) -> Snapshot {
        let language = L10nLanguage.all.first { $0.code == code } ?? L10nLanguage.english
        let english = lproj(L10nLanguage.english.apple) ?? resourceRoot()
        return Snapshot(language: language, locale: Locale(identifier: language.tag),
                        bundle: lproj(language.apple) ?? english, english: english)
    }

    /// The current snapshot; reading it registers an Observation dependency.
    var current: Snapshot {
        registrar.access(self, keyPath: \.current)
        return state.withLock { $0 }
    }

    /// The language's code ("pt"): what goes on the wire.
    var code: String { current.language.code }
    /// The locale every formatter that returns text for the user takes.
    var locale: Locale { current.locale }

    /// Switches the language; a code this build doesn't have means English.
    /// SwiftUI draws the views again from the mutation.
    @MainActor
    func set(_ code: String) {
        let next = L10n.snapshot(for: code)
        registrar.withMutation(of: self, keyPath: \.current) { state.withLock { $0 = next } }
    }

    /// The system's language among this build's: the whole preference list
    /// counts ([ca-ES, es-ES] gives es, pt-BR gives pt, ja gives en). On iOS
    /// the list starts with the language picked for the app in Settings.
    static func systemLanguage(_ preferences: [String] = Locale.preferredLanguages) -> String {
        let languages = L10nLanguage.all.filter { !$0.pseudo }
        let best = Bundle.preferredLocalizations(from: languages.map(\.apple), forPreferences: preferences).first
        return languages.first { $0.apple == best }?.code ?? L10nLanguage.english.code
    }

    /// A key's text from one table, formatted. Falls back to English, then to
    /// the key itself.
    func format(_ key: String, table: String, _ args: [any CVarArg]) -> String {
        let s = current
        var template = s.bundle.localizedString(forKey: key, value: notFound, table: table)
        // A format string with no text in a language compiles to key = key.
        if template == notFound || template == key {
            template = s.english.localizedString(forKey: key, value: notFound, table: table)
            if template == notFound {
                i18nLog.error("missing key \(key, privacy: .public) in table \(table, privacy: .public)")
                template = key
            }
        }
        // The format must stay the string the lookup returned: a copy
        // ("\(template)") loses the .stringsdict rules that pick a plural form.
        return String(format: template, locale: s.locale, arguments: args)
    }

    /// A user-controlled text (a name, a file name) wrapped in Unicode
    /// isolation marks, so that it can't reorder the sentence around it.
    static func isolate(_ text: String) -> String { "\u{2068}" + text + "\u{2069}" }
}

private let notFound = "\u{1}\u{1}"
private let i18nLog = Logger(subsystem: "cloud.off-the.OffTheCloud", category: "i18n")

/// The generated accessors (S+<Table>.swift) extend this namespace.
enum S {}

/// A key's text in the current language. Generated accessors call it with
/// the key's table and its arguments in their declared order (Strings for
/// %N$@, Ints for %N$lld and the plural count).
func L(_ key: String, table: String, _ args: any CVarArg...) -> String {
    L10n.shared.format(key, table: table, args)
}

/// An argument of a rich text.
enum L10nArg {
    case text(String)
    case int(Int)
}

// Rich text: a key whose text marks spans with tags, "Read the
// <link>privacy policy</link>", rendered into an AttributedString. The tags
// are found in the formatted text before any argument is in it:
//
//   1. Each text argument is passed to the format as a stand-in, the single
//      private-use character U+E000 + its index; numbers are formatted as
//      they are (digits and separators carry no markup).
//   2. The formatted text - the language's own wording, plural form and
//      argument order - is split into tags and text. The catalog refuses a
//      literal "<" and every private-use character, so a "<" can only
//      start one of the key's tags and a private-use character can only be
//      a stand-in.
//   3. Each stand-in is replaced by its argument as a plain run, styled by
//      the tag around it but never read for tags.
//
// Arguments are therefore inserted once and never parsed: a name such as
// "<link>" or one holding U+E000 comes out exactly as typed. A "<" that
// doesn't open or close one of the key's tags in order stays text.

/// A rich key's text in the current language, each tag's span styled with
/// that tag's container (the code decides what a tag does: a link's address
/// always comes from code).
func LRich(_ key: String, table: String, tags: [String: AttributeContainer], _ args: [L10nArg]) -> AttributedString {
    var texts: [String] = []
    var formatArgs: [any CVarArg] = []
    for arg in args {
        switch arg {
        case .text(let s):
            formatArgs.append(L10n.standIn(texts.count))
            texts.append(s)
        case .int(let n):
            formatArgs.append(n)
        }
    }
    return L10n.markup(L10n.shared.format(key, table: table, formatArgs), texts: texts, tags: tags)
}

extension L10n {
    private static let standInBase: UInt32 = 0xE000

    /// The stand-in for the text argument at index.
    static func standIn(_ index: Int) -> String {
        String(Character(Unicode.Scalar(standInBase + UInt32(index))!))
    }

    /// Splits a formatted rich text into tags and text, and puts the text
    /// arguments in place of their stand-ins.
    static func markup(_ formatted: String, texts: [String], tags: [String: AttributeContainer]) -> AttributedString {
        let scalars = Array(formatted.unicodeScalars)
        var out = AttributedString()
        var run = String.UnicodeScalarView()
        var inside: (name: String, style: AttributeContainer)?
        func flush() {
            if !run.isEmpty {
                out.append(AttributedString(String(run), attributes: inside?.style ?? AttributeContainer()))
                run = String.UnicodeScalarView()
            }
        }
        var i = 0
        while i < scalars.count {
            let c = scalars[i]
            if c == "<", let end = scalars[i...].firstIndex(of: ">") {
                let inner = String(String.UnicodeScalarView(scalars[(i + 1)..<end]))
                let closing = inner.hasPrefix("/")
                let name = closing ? String(inner.dropFirst()) : inner
                if let style = tags[name], closing ? inside?.name == name : inside == nil {
                    flush()
                    inside = closing ? nil : (name, style)
                    i = end + 1
                    continue
                }
            }
            if c.value >= standInBase, c.value - standInBase < UInt32(texts.count) {
                flush()
                out.append(AttributedString(texts[Int(c.value - standInBase)], attributes: inside?.style ?? AttributeContainer()))
                i += 1
                continue
            }
            run.append(c)
            i += 1
        }
        flush()
        return out
    }
}
