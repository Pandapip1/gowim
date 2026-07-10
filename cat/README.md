# gowim/cat

A Go implementation of the on-disk **structure** of Windows Catalog (`.cat`)
files: parsing and serialization of the PKCS #7 envelope and the embedded
Microsoft Certificate Trust List (CTL), used to check driver-package files
(`.inf`/`.cat`/`.sys`) against their whitelisted hashes.

Structural facts are cross-checked against
[RFC 2315](https://www.rfc-editor.org/rfc/rfc2315.html) (PKCS #7), Microsoft's
[MS-CAESO](https://winprotocoldoc.blob.core.windows.net/productionwindowsarchives/WinArchive/%5bMS-CAESO%5d.pdf)
protocol document, the ["Windows Authenticode Portable Executable Signature
Format"](https://download.microsoft.com/download/9/c/5/9c5b2167-8017-4bae-9fde-d599bac8184a/authenticode_pe.docx)
spec, and [ralphje/signify](https://github.com/ralphje/signify)'s
`signify/asn1/ctl.py` and `signify/asn1/spc.py`, which implement and cite the
same structures. See `oids.go` for OID-by-OID citations.

## Scope

This package handles the **container structure** of a `.cat` file:

- the outer PKCS #7 `ContentInfo`/`SignedData` envelope (`ContentInfo`,
  `SignedData`)
- digest algorithm identifiers (`crypto/x509/pkix.AlgorithmIdentifier`)
- the embedded Microsoft CTL (`CertificateTrustList`), parsed far enough to
  expose each member's tag and PKCS #9-style attributes
  (`CatalogMember`, `Attribute`)
- the two attribute shapes needed to check a driver package: the
  `CAT_NAMEVALUE_OBJID` name/value pairs, e.g. `File=...` (`NameValue`,
  `CatalogMember.NameValues`, `CatalogMember.File`), and the per-file digest
  algorithm + hash carried by `SPC_INDIRECT_DATA_OBJID`
  (`CatalogMember.Digest`)

It **deliberately does not** implement:

- Authenticode/PKCS #7 signature verification -- `SignerInfos` is never
  checked against a message digest or public key.
- X.509 certificate parsing or chain validation -- `SignedData.Certificates`
  and `SignedData.CRLs` are preserved as opaque, individually
  round-trippable DER blobs, not decoded.
- Creating a new, validly-signed catalog from scratch -- that requires a real
  code-signing key. `AppendTo` re-encodes a parsed structure back to DER; it
  does not create or alter any signature.

This mirrors the sibling `wim` package's explicit non-goal of not
implementing the LZX/XPRESS/LZMS compression codecs: here the corresponding
out-of-scope "codec" is Authenticode/X.509 trust evaluation, not container
structure.

## Layout

Everything lives in a single package, `cat`, one file per format concern:

| File | Responsibility |
|------|-----------------|
| `cat.go` | package doc, shared helpers |
| `oids.go` | named, cited OID constants |
| `der.go` | BMPString / UTF-16LE helpers |
| `signeddata.go` | `ContentInfo`, `SignedData` (the outer PKCS #7 envelope) |
| `ctl.go` | `CertificateTrustList`, `CatalogMember`, `Attribute`, `NameValue`, digest extraction |
| `cat_test.go` | a hand-built synthetic catalog + round-trip tests |

## Usage

```go
data, _ := os.ReadFile("driver.cat")

ci, err := cat.ParseContentInfo(data)
if err != nil {
    log.Fatal(err)
}
if ci.SignedData == nil || ci.SignedData.CTL == nil {
    log.Fatal("not a catalog file")
}

for _, m := range ci.SignedData.CTL.Members {
    name, ok, err := m.File()
    if err != nil {
        log.Fatal(err)
    }
    if !ok {
        continue
    }
    algo, hash, ok, err := m.Digest()
    if err != nil {
        log.Fatal(err)
    }
    if ok {
        fmt.Printf("%s: %s %x\n", name, algo, hash)
    }
}
```

Each component also parses from and serializes to a byte slice directly
(`Parse*` functions and `AppendTo` methods).

## Tests

```
go test ./...
```

Since fetching a real signed `.cat` file was out of scope for this exercise,
`cat_test.go` hand-constructs a minimal, synthetic (unsigned) catalog: a
`SignedData` wrapping a `CertificateTrustList` with two members, each
carrying a `File` name/value attribute and a SHA-1 digest attribute. Tests
assert that `Parse` recovers the encoded members, name/value pairs, and
digests, and that `Parse` followed by `AppendTo` reproduces the original DER
bytes exactly (DER is canonical, so this round trip is byte-for-byte, not
just semantic) -- including when the opaque `Certificates`/`CRLs` fields are
populated, and for an unrecognized outer content type.

## License

MIT OR Apache-2.0.
