package driver

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/asn1"
	"errors"
	"io/fs"

	"github.com/gavin-john/gowim/cat"
)

// VerifyStatus classifies the result of comparing one payload file's hash
// against the package's catalog.
type VerifyStatus int

const (
	// VerifyOK means the file's hash, under the catalog's own digest
	// algorithm, matches the catalog member's recorded digest.
	VerifyOK VerifyStatus = iota
	// VerifyMismatch means a catalog member was found for the file but its
	// recorded digest does not match the file's actual hash.
	VerifyMismatch
	// VerifyNotInCatalog means no catalog member's File name-value attribute
	// matches this payload file's source or destination name.
	VerifyNotInCatalog
	// VerifyUnsupportedAlgorithm means a catalog member was found, but its
	// digest algorithm is neither SHA-1 nor SHA-256 (the only algorithms
	// this package computes).
	VerifyUnsupportedAlgorithm
)

// String returns a short human-readable label for the status.
func (s VerifyStatus) String() string {
	switch s {
	case VerifyOK:
		return "ok"
	case VerifyMismatch:
		return "mismatch"
	case VerifyNotInCatalog:
		return "not in catalog"
	case VerifyUnsupportedAlgorithm:
		return "unsupported digest algorithm"
	default:
		return "unknown"
	}
}

// FileVerification is the result of checking one PayloadFile against the
// package's catalog.
type FileVerification struct {
	// File is the payload file that was checked.
	File PayloadFile
	// Status classifies the result; see VerifyStatus.
	Status VerifyStatus
	// Algorithm is the catalog member's digest algorithm OID (cat.OIDSHA1 or
	// cat.OIDSHA256), populated whenever a catalog member was found.
	Algorithm asn1.ObjectIdentifier
	// Computed is the file's actual digest under Algorithm, populated
	// whenever a catalog member with a supported algorithm was found.
	Computed []byte
	// Expected is the catalog member's recorded digest, populated whenever a
	// catalog member was found.
	Expected []byte
}

// Verify computes each payload file's hash and compares it against the
// corresponding cat.CatalogMember's digest, matched by the member's "File"
// name-value attribute against the payload file's SourceName (tried first,
// since catalogs conventionally record the as-shipped source file name) or
// DestName. It supports whichever of SHA-1 or SHA-256 the catalog member's
// digest algorithm identifies (see cat.CatalogMember.Digest); any other
// algorithm is reported as VerifyUnsupportedAlgorithm without treating it as
// a fatal error.
//
// It returns an error only for a problem verifying is impossible to attempt
// at all (no catalog loaded, or a payload file's bytes could not be read),
// not for a hash mismatch or an uncataloged file - those are reported per
// file in the returned slice.
func (p *Package) Verify() ([]FileVerification, error) {
	if p.Catalog == nil || p.Catalog.CTL == nil {
		return nil, wrapErr("verify", errors.New("package has no catalog loaded"))
	}

	membersByFile := make(map[string]*cat.CatalogMember, len(p.Catalog.CTL.Members))
	for i := range p.Catalog.CTL.Members {
		m := &p.Catalog.CTL.Members[i]
		name, ok, err := m.File()
		if err != nil {
			return nil, wrapErr("catalog member file name", err)
		}
		if ok {
			membersByFile[normalizeFileKey(name)] = m
		}
	}

	out := make([]FileVerification, 0, len(p.Files))
	for _, pf := range p.Files {
		data, err := fs.ReadFile(p.FSys, pf.SourcePath)
		if err != nil {
			return nil, wrapErr("read payload file "+pf.SourcePath, err)
		}

		member, ok := membersByFile[normalizeFileKey(pf.SourceName)]
		if !ok {
			member, ok = membersByFile[normalizeFileKey(pf.DestName)]
		}
		if !ok {
			out = append(out, FileVerification{File: pf, Status: VerifyNotInCatalog})
			continue
		}

		algo, expected, ok, err := member.Digest()
		if err != nil {
			return nil, wrapErr("catalog member digest for "+pf.DestName, err)
		}
		if !ok {
			out = append(out, FileVerification{File: pf, Status: VerifyNotInCatalog})
			continue
		}

		fv := FileVerification{File: pf, Algorithm: algo, Expected: expected}
		var computed []byte
		switch {
		case algo.Equal(cat.OIDSHA1):
			h := sha1.Sum(data)
			computed = h[:]
		case algo.Equal(cat.OIDSHA256):
			h := sha256.Sum256(data)
			computed = h[:]
		default:
			fv.Status = VerifyUnsupportedAlgorithm
			out = append(out, fv)
			continue
		}
		fv.Computed = computed
		if bytes.Equal(computed, expected) {
			fv.Status = VerifyOK
		} else {
			fv.Status = VerifyMismatch
		}
		out = append(out, fv)
	}
	return out, nil
}

// normalizeFileKey lower-cases a file name for case-insensitive lookup
// (Windows file names are case-insensitive), and strips any directory
// component a catalog member's File attribute might carry (some catalogs
// record a path like "x64\driver.sys" rather than a bare name).
func normalizeFileKey(name string) string {
	if i := lastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	return toLowerASCII(name)
}

func lastIndexAny(s, chars string) int {
	for i := len(s) - 1; i >= 0; i-- {
		for j := 0; j < len(chars); j++ {
			if s[i] == chars[j] {
				return i
			}
		}
	}
	return -1
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
