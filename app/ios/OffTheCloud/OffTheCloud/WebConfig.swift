import SwiftUI
import WebKit

struct WebConfig {
  let endpoint: String
  let password: String
  let deviceId: String
}

struct WebContainerView: UIViewRepresentable {
  let config: WebConfig
  @Binding var showSettings: Bool
  
  func makeCoordinator() -> Coordinator { Coordinator(self) }

  func makeUIView(context: Context) -> WKWebView {
    // 1) Point to your bundled "web-dist" folder (blue folder reference)
    guard let base = Bundle.main.resourceURL?.appendingPathComponent("web-dist", isDirectory: true) else {
      fatalError("web-dist not found in bundle. Add your Vite dist as a blue folder named 'web-dist'.")
    }

    // 2) Configure WKWebView with app:// scheme
    let wk = WKWebViewConfiguration()
    wk.setURLSchemeHandler(AppSchemeHandler(baseURL: base), forURLScheme: "app")

    // 3) Inject config safely as JSON (before any script runs)
    let cfg: [String: String] = [
      "endpoint": config.endpoint,
      "password": config.password,
      "deviceId": config.deviceId
    ]
    let cfgData = try! JSONSerialization.data(withJSONObject: cfg)
    let cfgJSON = String(data: cfgData, encoding: .utf8)!
    let injectCfg = WKUserScript(
      source: "window.__OTC_CONFIG=\(cfgJSON);",
      injectionTime: .atDocumentStart,
      forMainFrameOnly: true
    )
    wk.userContentController.addUserScript(injectCfg)
    wk.userContentController.add(context.coordinator, name: "native")

    // 4) (Optional) Console/Errors bridge to Xcode
      wk.userContentController.add(context.coordinator, name: "log")
    let logShim = """
      (function(){
        const send=t=>window.webkit?.messageHandlers?.log?.postMessage(t);
        ['log','warn','error'].forEach(k=>{
          const o=console[k];
          console[k]=function(...a){ try{send('['+k+'] '+a.join(' '))}catch(e){}; o.apply(console,a); };
        });
        window.addEventListener('error', e=>send('[onerror] '+(e.message||e)));
        window.addEventListener('unhandledrejection', e=>send('[unhandled] '+String(e.reason||e)));
      })();
    """
    wk.userContentController.addUserScript(.init(source: logShim, injectionTime: .atDocumentStart, forMainFrameOnly: true))

    // 5) WebView
    let web = WKWebView(frame: .zero, configuration: wk)
    web.navigationDelegate = context.coordinator
    web.uiDelegate = context.coordinator
    if #available(iOS 16.4, *) { web.isInspectable = true }
      
    web.scrollView.pinchGestureRecognizer?.isEnabled = false
    web.scrollView.gestureRecognizers?.forEach {
      if let tap = $0 as? UITapGestureRecognizer, tap.numberOfTapsRequired == 2 {
        $0.isEnabled = false
      }
    }

    // 6) Load your app
    let start = URL(string: "app://index.html")!
    web.load(URLRequest(url: start))
    return web
  }

  func updateUIView(_ uiView: WKWebView, context: Context) {}

  final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandler {
    let parent: WebContainerView
    init(_ parent: WebContainerView) { self.parent = parent }
      
    // alerts/prompts from JS
    func webView(_ webView: WKWebView,
                 runJavaScriptAlertPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo,
                 completionHandler: @escaping () -> Void) {
      let ac = UIAlertController(title: nil, message: message, preferredStyle: .alert)
      ac.addAction(UIAlertAction(title: "OK", style: .default) { _ in completionHandler() })
      topController()?.present(ac, animated: true)
    }
    func webView(_ webView: WKWebView,
                 runJavaScriptConfirmPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo,
                 completionHandler: @escaping (Bool) -> Void) {
      let ac = UIAlertController(title: nil, message: message, preferredStyle: .alert)
      ac.addAction(UIAlertAction(title: "Cancel", style: .cancel) { _ in completionHandler(false) })
      ac.addAction(UIAlertAction(title: "OK", style: .default) { _ in completionHandler(true) })
      topController()?.present(ac, animated: true)
    }
    func webView(_ webView: WKWebView,
                 runJavaScriptTextInputPanelWithPrompt prompt: String,
                 defaultText: String?,
                 initiatedByFrame frame: WKFrameInfo,
                 completionHandler: @escaping (String?) -> Void) {
      let ac = UIAlertController(title: nil, message: prompt, preferredStyle: .alert)
      ac.addTextField { $0.text = defaultText }
      ac.addAction(UIAlertAction(title: "Cancel", style: .cancel) { _ in completionHandler(nil) })
      ac.addAction(UIAlertAction(title: "OK", style: .default) { _ in completionHandler(ac.textFields?.first?.text) })
      topController()?.present(ac, animated: true)
    }

    // receive console logs
    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        print("JS:", message.body)
        guard message.name == "native" else { return }

        if let dict = message.body as? [String: Any],
           let action = dict["action"] as? String {
            switch action {
            case "openSettings":
                self.parent.showSettings = true
            default:
                break
            }
        }
    }

    private func topController() -> UIViewController? {
      guard let scene = UIApplication.shared.connectedScenes.first as? UIWindowScene,
            let window = scene.windows.first(where: { $0.isKeyWindow }),
            var top = window.rootViewController else { return nil }
      while let presented = top.presentedViewController { top = presented }
      return top
    }
  }
}
