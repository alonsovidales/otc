//
//  AppSchemeHandler.swift
//  OffTheCloud
//
//  Created by Alonso Vidales on 8/9/25.
//


import Foundation
import WebKit

final class AppSchemeHandler: NSObject, WKURLSchemeHandler {
  private let baseURL: URL

  init(baseURL: URL) {
    self.baseURL = baseURL
    super.init()
  }

  func webView(_ webView: WKWebView, start urlSchemeTask: WKURLSchemeTask) {
    guard let url = urlSchemeTask.request.url else { return }
    // Map app:// → files inside baseURL
    // e.g. app://index.html         -> baseURL/index.html
    //      app://assets/a.js        -> baseURL/assets/a.js
    //      app://                   -> baseURL/index.html
    let path = url.path.isEmpty ? "index.html" : url.path.dropFirst() // remove leading "/"
    let fileURL = baseURL.appendingPathComponent(String(path), isDirectory: false)

    do {
      let data = try Data(contentsOf: fileURL)
      let mime = Self.mimeType(forPath: fileURL.path)
      let headers = ["Content-Type": mime]
      let response = HTTPURLResponse(url: url, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: headers)!
      urlSchemeTask.didReceive(response)
      urlSchemeTask.didReceive(data)
      urlSchemeTask.didFinish()
    } catch {
      let response = HTTPURLResponse(url: url, statusCode: 404, httpVersion: "HTTP/1.1", headerFields: nil)!
      urlSchemeTask.didReceive(response)
      urlSchemeTask.didFinish()
    }
  }

  func webView(_ webView: WKWebView, stop urlSchemeTask: WKURLSchemeTask) {
    // nothing to cancel (we load synchronously from bundle)
  }

  private static func mimeType(forPath path: String) -> String {
    switch (path as NSString).pathExtension.lowercased() {
      case "html": return "text/html"
      case "js":   return "application/javascript"
      case "mjs":  return "application/javascript"
      case "css":  return "text/css"
      case "svg":  return "image/svg+xml"
      case "png":  return "image/png"
      case "jpg", "jpeg": return "image/jpeg"
      case "gif":  return "image/gif"
      case "woff": return "font/woff"
      case "woff2":return "font/woff2"
      case "ttf":  return "font/ttf"
      case "json": return "application/json"
      default:     return "application/octet-stream"
    }
  }
}
