// Package cat implements parsing and serialization of the on-disk
// *structure* of Windows Catalog (.cat) files.
//
// A .cat file is a DER-encoded PKCS #7 ContentInfo (RFC 2315 §7) whose
// content is a SignedData value (RFC 2315 §9.1) carrying, as its
// encapsulated content, a Microsoft Certificate Trust List (CTL) -- the
// structure Windows uses to whitelist the per-file hashes of a driver
// package (or any other signed file set) under szOID_CATALOG_LIST
// (1.3.6.1.4.1.311.12.1.1). See oids.go for OID sources.
//
// Scope: this package parses and re-serializes the container down to the
// level needed to enumerate a catalog's members and read out their name/value
// attributes (in particular the "File" entry) and their per-file digest
// algorithm + hash, exactly the data needed to check a driver package's files
// against a catalog. It does NOT implement:
//
//   - Authenticode / PKCS #7 signature verification. SignerInfos are parsed
//     only insofar as needed to preserve them; their signature values are
//     never checked against a message digest or a public key.
//   - X.509 certificate parsing or chain validation. Certificates and CRLs
//     are preserved as opaque, individually round-trippable DER blobs (see
//     SignedData.Certificates, SignedData.CRLs) -- crypto/x509 can be used
//     on them by a caller that needs that, but this package does not
//     interpret their contents.
//   - Creating a new, validly-signed catalog from scratch. That requires a
//     real code-signing key and is out of scope; this package only
//     re-encodes a parsed structure back to DER (see the AppendTo methods),
//     which does not change or add any signature.
//
// This mirrors the sibling wim package's stated non-goals (it does not
// implement the LZX/XPRESS/LZMS compression codecs): here, the corresponding
// "codec" that is deliberately out of scope is Authenticode/X.509 trust
// evaluation, not container structure.
package cat

import "fmt"

// wrapErr is a small helper for adding context to parse errors without
// pulling in a dependency.
func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("cat: %s: %w", what, err)
}
