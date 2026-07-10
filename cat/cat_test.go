package cat

import (
	"bytes"
	"crypto/sha1"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"testing"
	"time"
)

// buildNameValue hand-encodes a CAT_NAMEVALUE entry: a BMPString refname (as
// a raw tagged value, since encoding/asn1 cannot marshal a plain Go string as
// BMPString), an INTEGER typeaction, and an OCTET STRING value holding
// NUL-terminated UTF-16LE text -- mirroring nameValueASN1 in ctl.go.
func buildNameValue(t *testing.T, refname string, typeaction int32, value string) []byte {
	t.Helper()
	raw := nameValueASN1{
		RefName: asn1.RawValue{
			Class: asn1.ClassUniversal,
			Tag:   asn1.TagBMPString,
			Bytes: utf8ToBMPString(refname),
		},
		TypeAction: typeaction,
		Value:      stringToUTF16LE(value),
	}
	b, err := asn1.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal name value: %v", err)
	}
	return b
}

// buildIndirectData hand-encodes an SpcIndirectDataContent carrying a
// member's digest algorithm and hash, mirroring
// spcIndirectDataContentASN1 in ctl.go.
func buildIndirectData(t *testing.T, algorithm asn1.ObjectIdentifier, hash []byte) []byte {
	t.Helper()
	idc := spcIndirectDataContentASN1{
		Data: spcAttributeTypeAndOptionalValueASN1{
			Type: OIDCatalogListMember,
		},
		MessageDigest: digestInfoASN1{
			DigestAlgorithm: pkix.AlgorithmIdentifier{Algorithm: algorithm},
			Digest:          hash,
		},
	}
	b, err := asn1.Marshal(idc)
	if err != nil {
		t.Fatalf("marshal indirect data content: %v", err)
	}
	return b
}

// buildMember constructs a CatalogMember with a File name/value attribute
// and a digest attribute for the given file name and hash.
func buildMember(t *testing.T, tag []byte, fileName string, hash []byte) CatalogMember {
	t.Helper()
	nameValues := []asn1.RawValue{
		{FullBytes: buildNameValue(t, "File", 0x10010001, fileName)},
		{FullBytes: buildNameValue(t, "OSAttr", 0x10010001, "2:6.1,2:10.0")},
	}
	digestValues := []asn1.RawValue{
		{FullBytes: buildIndirectData(t, OIDSHA1, hash)},
	}
	return CatalogMember{
		Tag: tag,
		Attributes: []Attribute{
			{Type: OIDCatNameValue, Values: nameValues},
			{Type: OIDSpcIndirectDataContent, Values: digestValues},
		},
	}
}

// buildCatalog hand-constructs a minimal, synthetic (unsigned) catalog file:
// a PKCS #7 ContentInfo/SignedData envelope wrapping a CertificateTrustList
// with two members, each carrying a File name/value attribute and a SHA-1
// digest attribute.
func buildCatalog(t *testing.T) *ContentInfo {
	t.Helper()

	hash1 := sha1.Sum([]byte("driver.sys"))
	hash2 := sha1.Sum([]byte("driver.inf"))

	ctl := &CertificateTrustList{
		SubjectUsage:     []asn1.ObjectIdentifier{OIDCatalogList},
		ListIdentifier:   []byte{0xaa, 0xbb, 0xcc, 0xdd},
		SequenceNumber:   big.NewInt(1),
		ThisUpdate:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		SubjectAlgorithm: pkix.AlgorithmIdentifier{Algorithm: OIDSHA1},
		Members: []CatalogMember{
			buildMember(t, []byte{0x01, 0x02, 0x03, 0x04}, "driver.sys", hash1[:]),
			buildMember(t, []byte{0x05, 0x06, 0x07, 0x08}, "driver.inf", hash2[:]),
		},
	}

	sd := &SignedData{
		Version:          1,
		DigestAlgorithms: []pkix.AlgorithmIdentifier{{Algorithm: OIDSHA1}},
		ContentType:      OIDCatalogList,
		CTL:              ctl,
		// A real catalog's signerInfos is a non-empty SET OF SignerInfo, but
		// this package neither produces nor verifies signatures, so an
		// empty SET (DER: tag 0x31, length 0) is a structurally valid
		// stand-in that still exercises the round trip.
		SignerInfos: []byte{0x31, 0x00},
	}

	return &ContentInfo{
		ContentType: OIDSignedData,
		SignedData:  sd,
	}
}

func TestParseCatalogMembers(t *testing.T) {
	orig := buildCatalog(t)
	der, err := orig.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}

	ci, err := ParseContentInfo(der)
	if err != nil {
		t.Fatalf("ParseContentInfo: %v", err)
	}
	if !ci.ContentType.Equal(OIDSignedData) {
		t.Fatalf("content type = %v, want signedData", ci.ContentType)
	}
	if ci.SignedData == nil {
		t.Fatal("SignedData is nil")
	}
	sd := ci.SignedData
	if !sd.ContentType.Equal(OIDCatalogList) {
		t.Fatalf("encapsulated content type = %v, want catalog list", sd.ContentType)
	}
	if sd.CTL == nil {
		t.Fatal("CTL is nil")
	}
	if len(sd.CTL.Members) != 2 {
		t.Fatalf("got %d members, want 2", len(sd.CTL.Members))
	}

	wantFiles := []string{"driver.sys", "driver.inf"}
	wantHashes := [][]byte{}
	{
		h1 := sha1.Sum([]byte("driver.sys"))
		h2 := sha1.Sum([]byte("driver.inf"))
		wantHashes = append(wantHashes, h1[:], h2[:])
	}

	for i, m := range sd.CTL.Members {
		name, ok, err := m.File()
		if err != nil {
			t.Fatalf("member %d: File: %v", i, err)
		}
		if !ok {
			t.Fatalf("member %d: no File entry", i)
		}
		if name != wantFiles[i] {
			t.Fatalf("member %d: File = %q, want %q", i, name, wantFiles[i])
		}

		nvs, err := m.NameValues()
		if err != nil {
			t.Fatalf("member %d: NameValues: %v", i, err)
		}
		if len(nvs) != 2 {
			t.Fatalf("member %d: got %d name values, want 2", i, len(nvs))
		}
		if nvs[1].Name != "OSAttr" || nvs[1].Value != "2:6.1,2:10.0" {
			t.Fatalf("member %d: OSAttr entry = %+v", i, nvs[1])
		}

		algo, hash, ok, err := m.Digest()
		if err != nil {
			t.Fatalf("member %d: Digest: %v", i, err)
		}
		if !ok {
			t.Fatalf("member %d: no digest attribute", i)
		}
		if !algo.Equal(OIDSHA1) {
			t.Fatalf("member %d: digest algorithm = %v, want SHA-1", i, algo)
		}
		if !bytes.Equal(hash, wantHashes[i]) {
			t.Fatalf("member %d: hash = %x, want %x", i, hash, wantHashes[i])
		}
	}
}

func TestRoundTrip(t *testing.T) {
	orig := buildCatalog(t)
	der1, err := orig.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}

	parsed, err := ParseContentInfo(der1)
	if err != nil {
		t.Fatalf("ParseContentInfo: %v", err)
	}

	der2, err := parsed.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo (reserialize): %v", err)
	}

	if !bytes.Equal(der1, der2) {
		t.Fatalf("round trip not byte-identical:\n original: % x\nreserialized: % x", der1, der2)
	}

	// A second parse/append cycle should also be a no-op (idempotency).
	parsed2, err := ParseContentInfo(der2)
	if err != nil {
		t.Fatalf("ParseContentInfo (2nd): %v", err)
	}
	der3, err := parsed2.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo (2nd reserialize): %v", err)
	}
	if !bytes.Equal(der2, der3) {
		t.Fatalf("second round trip not byte-identical")
	}
}

func TestSignedDataOpaqueFieldsPreserved(t *testing.T) {
	orig := buildCatalog(t)
	orig.SignedData.Certificates = []byte{0xa0, 0x03, 0x02, 0x01, 0x2a} // [0] { INTEGER 42 }, a nonsense but well-formed placeholder
	orig.SignedData.CRLs = []byte{0xa1, 0x00}                           // [1] {} empty

	der, err := orig.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}
	ci, err := ParseContentInfo(der)
	if err != nil {
		t.Fatalf("ParseContentInfo: %v", err)
	}
	if !bytes.Equal(ci.SignedData.Certificates, orig.SignedData.Certificates) {
		t.Fatalf("certificates not preserved: got % x, want % x", ci.SignedData.Certificates, orig.SignedData.Certificates)
	}
	if !bytes.Equal(ci.SignedData.CRLs, orig.SignedData.CRLs) {
		t.Fatalf("crls not preserved: got % x, want % x", ci.SignedData.CRLs, orig.SignedData.CRLs)
	}

	der2, err := ci.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo (reserialize): %v", err)
	}
	if !bytes.Equal(der, der2) {
		t.Fatalf("round trip with opaque fields not byte-identical")
	}
}

func TestUnrecognizedContentTypeRoundTrips(t *testing.T) {
	ci := &ContentInfo{
		ContentType: asn1.ObjectIdentifier{1, 2, 3, 4, 5},
		Content:     []byte{0x02, 0x01, 0x07}, // INTEGER 7, an arbitrary opaque payload
	}
	der, err := ci.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo: %v", err)
	}
	parsed, err := ParseContentInfo(der)
	if err != nil {
		t.Fatalf("ParseContentInfo: %v", err)
	}
	if parsed.SignedData != nil {
		t.Fatal("SignedData should be nil for an unrecognized content type")
	}
	if !bytes.Equal(parsed.Content, ci.Content) {
		t.Fatalf("content = % x, want % x", parsed.Content, ci.Content)
	}
	der2, err := parsed.AppendTo(nil)
	if err != nil {
		t.Fatalf("AppendTo (reserialize): %v", err)
	}
	if !bytes.Equal(der, der2) {
		t.Fatal("round trip not byte-identical")
	}
}
