import Foundation
import Security

// The bridge relays already-decrypted app payloads between a device and a
// friend/browser client, so a plaintext password in the protobuf envelope
// would be readable by the bridge operator. To keep the account password
// confidential end-to-end (see issue #2), the device generates an ephemeral
// RSA keypair per WebSocket connection and hands out the public half via
// GetPubKey/PubKey; the client encrypts the password with it
// (RSA-OAEP/SHA-256) before it ever leaves the app.

enum PwCryptoError: Error {
    case malformedPublicKey
    case importFailed(String)
    case encryptionUnsupported
    case encryptionFailed(String)
}

enum PwCrypto {
    /// Encrypts `password` with the PKIX/SPKI DER-encoded RSA public key
    /// returned by the device's GetPubKey/PubKey response.
    static func encryptPassword(_ password: String, pubKeyDER: Data) throws -> Data {
        let pkcs1 = try rsaPublicKeyPKCS1(fromSPKI: pubKeyDER)

        let attrs: [CFString: Any] = [
            kSecAttrKeyType: kSecAttrKeyTypeRSA,
            kSecAttrKeyClass: kSecAttrKeyClassPublic,
        ]
        var error: Unmanaged<CFError>?
        guard let secKey = SecKeyCreateWithData(pkcs1 as CFData, attrs as CFDictionary, &error) else {
            throw PwCryptoError.importFailed((error?.takeRetainedValue() as Error?)?.localizedDescription ?? "unknown error")
        }

        guard SecKeyIsAlgorithmSupported(secKey, .encrypt, .rsaEncryptionOAEPSHA256) else {
            throw PwCryptoError.encryptionUnsupported
        }

        var encError: Unmanaged<CFError>?
        guard let cipherText = SecKeyCreateEncryptedData(
            secKey,
            .rsaEncryptionOAEPSHA256,
            Data(password.utf8) as CFData,
            &encError
        ) as Data? else {
            throw PwCryptoError.encryptionFailed((encError?.takeRetainedValue() as Error?)?.localizedDescription ?? "unknown error")
        }

        return cipherText
    }

    /// The device sends the public key as X.509 SubjectPublicKeyInfo (what
    /// Go's x509.MarshalPKIXPublicKey produces), but SecKeyCreateWithData
    /// expects the bare PKCS#1 RSAPublicKey bytes. Strip the SPKI wrapper:
    /// SEQUENCE { AlgorithmIdentifier, BIT STRING { <PKCS#1 bytes> } }.
    private static func rsaPublicKeyPKCS1(fromSPKI der: Data) throws -> Data {
        var reader = DERReader(data: der)
        let outer = try reader.readElement(expectedTag: 0x30) // SEQUENCE
        var inner = DERReader(data: outer)
        _ = try inner.readElement(expectedTag: 0x30) // AlgorithmIdentifier, skip it
        let bitString = try inner.readElement(expectedTag: 0x03) // BIT STRING
        guard let unusedBits = bitString.first, unusedBits == 0 else {
            throw PwCryptoError.malformedPublicKey
        }
        return bitString.dropFirst()
    }
}

/// Minimal DER TLV reader, just enough to unwrap an SPKI structure.
private struct DERReader {
    let data: Data
    var index: Int = 0

    init(data: Data) { self.data = data }

    mutating func readElement(expectedTag: UInt8) throws -> Data {
        guard index < data.count, data[data.startIndex + index] == expectedTag else {
            throw PwCryptoError.malformedPublicKey
        }
        index += 1

        guard index < data.count else { throw PwCryptoError.malformedPublicKey }
        let firstLenByte = data[data.startIndex + index]
        index += 1

        let length: Int
        if firstLenByte & 0x80 == 0 {
            length = Int(firstLenByte)
        } else {
            let numLenBytes = Int(firstLenByte & 0x7F)
            guard numLenBytes > 0, numLenBytes <= 4, index + numLenBytes <= data.count else {
                throw PwCryptoError.malformedPublicKey
            }
            var len = 0
            for _ in 0..<numLenBytes {
                len = (len << 8) | Int(data[data.startIndex + index])
                index += 1
            }
            length = len
        }

        guard index + length <= data.count else { throw PwCryptoError.malformedPublicKey }
        let start = data.startIndex + index
        let content = data.subdata(in: start..<(start + length))
        index += length
        return content
    }
}
