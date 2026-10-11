// SPDX-License-Identifier: AGPL-3.0-or-later

import Foundation
import Testing
import WebKit
@testable import OffTheCloud

/// The setup wizard speaks the app's language (docs/i18n.md, "Setup
/// wizard"): over Bluetooth only the path travels, so the page and every
/// /api call carry ?lang=. Android's SetupWizardLangTest is the same.
struct SetupWizardLangTests {
    @Test func thePageIsLoadedWithTheLanguage() {
        #expect(BLESetupLang.pageURL(code: "es").absoluteString == "otc-setup://device/?lang=es")
    }

    @Test func everyAPICallCarriesIt() {
        #expect(BLESetupLang.adding(to: "/api/state", code: "es") == "/api/state?lang=es")
        #expect(BLESetupLang.adding(to: "/api/scan?band=5", code: "fr") == "/api/scan?band=5&lang=fr")
        #expect(BLESetupLang.adding(to: "/api", code: "de") == "/api?lang=de")
    }

    @Test func aLanguageAlreadyThereAndOtherPathsAreLeftAlone() {
        #expect(BLESetupLang.adding(to: "/api/state?lang=fr", code: "es") == "/api/state?lang=fr")
        #expect(BLESetupLang.adding(to: "/", code: "es") == "/")
        #expect(BLESetupLang.adding(to: "/__otc/release", code: "es") == "/__otc/release")
        #expect(BLESetupLang.adding(to: "/apiary", code: "es") == "/apiary")
    }

    @Test func anOpenPageIsAskedToReWordItselfWithTheCodeQuoted() {
        #expect(BLESetupLang.switchScript(code: "es") == "window.otcSetupLang&&window.otcSetupLang(\"es\")")
        #expect(BLESetupLang.switchScript(code: "a\"b") == "window.otcSetupLang&&window.otcSetupLang(\"a\\\"b\")")
    }

    @Test func onlyABooleanAnswerMeansThePageSwitches() {
        #expect(BLESetupLang.pageSwitches(NSNumber(value: true)))
        // false: nothing to do (already in it).
        #expect(BLESetupLang.pageSwitches(NSNumber(value: false)))
        // An older wizard: undefined (nil), null, anything else.
        #expect(!BLESetupLang.pageSwitches(nil))
        #expect(!BLESetupLang.pageSwitches(NSNull()))
        #expect(!BLESetupLang.pageSwitches(NSNumber(value: 1)))
        #expect(!BLESetupLang.pageSwitches("true"))
    }

    @Test func aReloadWaitsForTheInstall() {
        #expect(!BLESetupLang.installStarted([:]))
        #expect(!BLESetupLang.installStarted(["install": ["phase": "idle"]]))
        #expect(BLESetupLang.installStarted(["install": ["phase": "installing"]]))
        #expect(BLESetupLang.installStarted(["install": ["phase": "failed"]]))
        #expect(BLESetupLang.installStarted(["install": ["phase": "idle"], "already_installed": true]))
    }

    @Test func thePagesLanguageIsItsAddresss() {
        #expect(BLESetupLang.lang(of: URL(string: "otc-setup://device/?lang=fr")) == "fr")
        #expect(BLESetupLang.lang(of: URL(string: "otc-setup://device/")) == nil)
        #expect(BLESetupLang.lang(of: nil) == nil)
    }
}

// MARK: the page in WebKit

/// Serves a stand-in setup page under otc-setup:// (the wizard's own scheme):
/// "/" has window.otcSetupLang, "/old" (an older image's wizard) has none.
/// Both note what sessionStorage held from before and keep a mark there.
private final class StandInSetupPage: NSObject, WKURLSchemeHandler {
    func webView(_ webView: WKWebView, start task: WKURLSchemeTask) {
        let url = task.request.url!
        let call = url.path == "/old" ? "" : "window.otcSetupLang=function(c){return c!=='en'};"
        let html = """
        <!doctype html><html><head><script>\(call)
        window.kept=(function(){try{var was=sessionStorage.getItem('otc-setup-keep');sessionStorage.setItem('otc-setup-keep','kept');return JSON.stringify({was:was,now:sessionStorage.getItem('otc-setup-keep')})}catch(e){return 'error: '+e}})();
        </script></head><body></body></html>
        """
        let body = Data(html.utf8)
        task.didReceive(HTTPURLResponse(url: url, statusCode: 200, httpVersion: "HTTP/1.1",
                                        headerFields: ["Content-Type": "text/html; charset=utf-8", "Content-Length": String(body.count)])!)
        task.didReceive(body)
        task.didFinish()
    }

    func webView(_ webView: WKWebView, stop task: WKURLSchemeTask) {}
}

/// Counts finished loads.
private final class Loads: NSObject, WKNavigationDelegate {
    var finished = 0
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { finished += 1 }
}

@MainActor
@Suite(.serialized)
struct SetupPageInWebKitTests {
    private let handler = StandInSetupPage()
    private let loads = Loads()

    private func makeView() -> WKWebView {
        let config = WKWebViewConfiguration()
        config.setURLSchemeHandler(handler, forURLScheme: BLESetupSchemeHandler.scheme)
        let view = WKWebView(frame: CGRect(x: 0, y: 0, width: 320, height: 480), configuration: config)
        view.navigationDelegate = loads
        return view
    }

    private func waitForLoad(_ n: Int) async {
        for _ in 0..<200 where loads.finished < n { try? await Task.sleep(for: .milliseconds(25)) }
    }

    /// The completion handler's result, as BLESetupTransport gets it.
    private func run(_ view: WKWebView, _ js: String) async -> Any? {
        await withCheckedContinuation { c in
            view.evaluateJavaScript(js) { result, _ in c.resume(returning: result) }
        }
    }

    @Test func sessionStorageKeepsThePagesStateAcrossItsOwnReload() async {
        let view = makeView()
        view.load(URLRequest(url: BLESetupLang.pageURL(code: "en")))
        await waitForLoad(1)
        #expect(await run(view, "window.kept") as? String == #"{"was":null,"now":"kept"}"#)
        // What the page's language switch does: location.replace with
        // another ?lang= on the same origin.
        _ = await run(view, "location.replace('otc-setup://device/?lang=fr');null")
        await waitForLoad(2)
        #expect(BLESetupLang.lang(of: view.url) == "fr")
        #expect(await run(view, "window.kept") as? String == #"{"was":"kept","now":"kept"}"#)
    }

    @Test func aWizardWithTheCallSwitchesAndAnOlderOneAnswersNothing() async {
        let view = makeView()
        view.load(URLRequest(url: BLESetupLang.pageURL(code: "en")))
        await waitForLoad(1)
        #expect(BLESetupLang.pageSwitches(await run(view, BLESetupLang.switchScript(code: "es"))))
        #expect(BLESetupLang.pageSwitches(await run(view, BLESetupLang.switchScript(code: "en"))))
        view.load(URLRequest(url: URL(string: "otc-setup://device/old?lang=en")!))
        await waitForLoad(2)
        #expect(!BLESetupLang.pageSwitches(await run(view, BLESetupLang.switchScript(code: "es"))))
    }
}
