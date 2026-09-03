//
//  FolderWatcher.swift
//  OffTheCloud
//
//  Issue #37: watch tracked folders for changes instead of periodically
//  re-scanning them from scratch. FSEvents is the same low-level mechanism
//  Dropbox/OneDrive/iCloud Drive use on macOS — it watches a directory tree
//  recursively at the OS level (no manual per-subdirectory registration)
//  and, with kFSEventStreamCreateFlagFileEvents, reports per-file
//  create/modify/remove/rename flags instead of just "something changed
//  somewhere under this directory" (the coarser default).
//
//  Swift has no native FSEvents API, so this wraps the C one directly.

import Foundation
import CoreServices

final class FolderWatcher {
    typealias Event = (path: String, flags: FSEventStreamEventFlags)

    private var streamRef: FSEventStreamRef?
    private var thread: Thread?
    private var runLoop: CFRunLoop?
    private let onEvents: ([Event]) -> Void

    init(onEvents: @escaping ([Event]) -> Void) {
        self.onEvents = onEvents
    }

    /// Starts watching `paths` (each a directory tree root, watched
    /// recursively). `latencySeconds` is how long FSEvents coalesces
    /// rapid-fire changes before delivering a batch — separate from (and
    /// smaller than) the debounce SyncModel applies per file on top.
    func start(paths: [String], latencySeconds: CFTimeInterval = 0.3) {
        guard streamRef == nil, !paths.isEmpty else { return }

        var context = FSEventStreamContext(
            version: 0,
            info: Unmanaged.passUnretained(self).toOpaque(),
            retain: nil,
            release: nil,
            copyDescription: nil
        )

        let flags = FSEventStreamCreateFlags(
            kFSEventStreamCreateFlagUseCFTypes |
            kFSEventStreamCreateFlagFileEvents |
            kFSEventStreamCreateFlagNoDefer
        )

        let callback: FSEventStreamCallback = { (_, clientCallBackInfo, numEvents, eventPaths, eventFlags, _) in
            guard let clientCallBackInfo else { return }
            let watcher = Unmanaged<FolderWatcher>.fromOpaque(clientCallBackInfo).takeUnretainedValue()
            // kFSEventStreamCreateFlagUseCFTypes makes eventPaths a
            // CFArray of CFStrings under the hood rather than a raw
            // char** — this is the standard way to recover it in Swift.
            let cfArray = unsafeBitCast(eventPaths, to: CFArray.self)
            guard let paths = cfArray as? [String] else { return }
            var events: [Event] = []
            events.reserveCapacity(numEvents)
            for i in 0..<numEvents {
                events.append((path: paths[i], flags: eventFlags[i]))
            }
            watcher.onEvents(events)
        }

        guard let stream = FSEventStreamCreate(
            kCFAllocatorDefault,
            callback,
            &context,
            paths as CFArray,
            FSEventStreamEventId(kFSEventStreamEventIdSinceNow),
            latencySeconds,
            flags
        ) else {
            return
        }
        streamRef = stream

        // FSEventStream needs a running CFRunLoop to pump callbacks on;
        // give it a dedicated background thread rather than sharing the
        // main run loop.
        let t = Thread { [weak self] in
            guard let self else { return }
            self.runLoop = CFRunLoopGetCurrent()
            FSEventStreamScheduleWithRunLoop(stream, self.runLoop!, CFRunLoopMode.defaultMode.rawValue)
            FSEventStreamStart(stream)
            CFRunLoopRun()
        }
        t.name = "otc.folder-watcher"
        t.start()
        thread = t
    }

    func stop() {
        guard let stream = streamRef else { return }
        FSEventStreamStop(stream)
        FSEventStreamInvalidate(stream)
        FSEventStreamRelease(stream)
        streamRef = nil
        if let rl = runLoop {
            CFRunLoopStop(rl)
        }
        runLoop = nil
        thread = nil
    }

    deinit {
        stop()
    }
}
