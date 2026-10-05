// SPDX-License-Identifier: AGPL-3.0-or-later

import Testing
@testable import OffTheCloud

struct PhotoSyncNamingTests {
    @Test func theUsualNameIsKept() {
        #expect(PhotoSync.remoteName("IMG_0001.HEIC", localIdentifier: "A/L0/001", alt: false) == "IMG_0001.HEIC")
    }

    @Test func theAlternateNameIsStableAndPerAsset() {
        let a = PhotoSync.remoteName("IMG_0001.HEIC", localIdentifier: "A/L0/001", alt: true)
        #expect(a == PhotoSync.remoteName("IMG_0001.HEIC", localIdentifier: "A/L0/001", alt: true))
        #expect(a != PhotoSync.remoteName("IMG_0001.HEIC", localIdentifier: "B/L0/001", alt: true))
        #expect(a.hasPrefix("IMG_0001_"))
        #expect(a.hasSuffix(".HEIC"))
        #expect(a.count == "IMG_0001_".count + 8 + ".HEIC".count)
    }

    @Test func aNameWithoutExtensionGetsTheSuffixAtTheEnd() {
        let a = PhotoSync.remoteName("scan", localIdentifier: "A/L0/001", alt: true)
        #expect(a.hasPrefix("scan_"))
        #expect(a.count == "scan_".count + 8)
    }

    @Test func recognizesTheDevicesDuplicateAnswer() {
        #expect(PhotoSync.isDuplicatedFile("error trying to link file: Duplicated file"))
        #expect(PhotoSync.isDuplicatedFile("error finishing the upload: Duplicated file"))
        #expect(!PhotoSync.isDuplicatedFile("error finishing the upload: upload incomplete: 1 of 2 bytes"))
    }
}
