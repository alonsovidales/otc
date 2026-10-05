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

    @Test func aNameTakenInTheRunIsSharedOnlyByTheSameContent() {
        let claims = PhotoSync.PathClaims()
        let path = "/ios/d/IMG_0001.HEIC"
        #expect(claims.claim(path, by: "A/L0/001", hash: "aa"))
        #expect(claims.claim(path, by: "A/L0/001", hash: "aa"))
        // A duplicate in Photos: same name, same bytes. It keeps the name
        // and the device answers with the existing row.
        #expect(claims.claim(path, by: "B/L0/001", hash: "aa"))
        // Another photo under that name goes to its alternate name.
        #expect(!claims.claim(path, by: "C/L0/001", hash: "cc"))
        #expect(claims.claim("/ios/d/IMG_0002.HEIC", by: "C/L0/001", hash: "cc"))
    }
}
