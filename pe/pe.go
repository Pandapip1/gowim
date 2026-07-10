// Package pe implements parsing and serialization of the container structure
// of the PE/COFF (Portable Executable / Common Object File Format) used by
// Windows binaries, including .sys kernel driver files.
//
// It is a from-scratch implementation cross-checked against the "Microsoft
// Portable Executable and Common Object File Format Specification" on
// Microsoft Learn / Windows Dev Center
// (https://learn.microsoft.com/en-us/windows/win32/debug/pe-format).
//
// Scope: this package handles the *container structure* of a PE image needed
// to validate that a file is a well-formed PE image, read its identifying
// fields (machine type, timestamp, checksum, section layout), and — most
// importantly for driver-signing workflows — locate the Attribute Certificate
// Table (the embedded Authenticode signature) referenced by the Security
// data directory, as a raw byte range. It implements:
//
//   - the MS-DOS stub header (IMAGE_DOS_HEADER), preserving the stub bytes
//     between the header and the PE header verbatim;
//   - the PE signature;
//   - the COFF file header (IMAGE_FILE_HEADER);
//   - the optional header, both the PE32 (IMAGE_OPTIONAL_HEADER32) and PE32+
//     (IMAGE_OPTIONAL_HEADER64) variants, and its data directory array;
//   - section headers (IMAGE_SECTION_HEADER) and section raw data, exposed as
//     opaque byte ranges;
//   - the Attribute Certificate Table: a sequence of WIN_CERTIFICATE entries,
//     exposed as (revision, type, raw bytes) — the certificate payload itself
//     is opaque to this package.
//
// This package deliberately does not implement: relocation, import, export,
// or debug-directory *semantic* parsing (their data directories are visible
// only as raw RVA/size pairs); disassembly of code sections; or Authenticode
// signature verification. In particular, the bCertificate payload of a
// WIN_CERTIFICATE entry is a PKCS#7 SignedData structure — this package
// exposes it only as raw bytes; structurally parsing that PKCS#7 content is
// the job of the sibling package github.com/gavin-john/gowim/cat, which this
// package does not import or depend on.
package pe

import (
	"encoding/binary"
	"fmt"
)

// le is the byte order used throughout the PE/COFF on-disk format.
var le = binary.LittleEndian

// wrapErr is a small helper for adding context to parse errors without
// pulling in a dependency.
func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("pe: %s: %w", what, err)
}
