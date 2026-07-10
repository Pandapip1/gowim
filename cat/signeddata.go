package cat

import (
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
)

// ContentInfo is the outer PKCS #7 envelope of a catalog file (RFC 2315 §7):
//
//	ContentInfo ::= SEQUENCE {
//	    contentType ContentType,                          -- OBJECT IDENTIFIER
//	    content     [0] EXPLICIT ANY DEFINED BY contentType OPTIONAL
//	}
//
// This package only interprets the signedData content type (OIDSignedData);
// SignedData is nil (and Content holds the raw, unparsed DER of whatever the
// content actually was) for any other contentType, so unrecognized inputs
// still round-trip byte-for-byte through Parse/AppendTo.
type ContentInfo struct {
	// ContentType identifies the shape of Content.
	ContentType asn1.ObjectIdentifier
	// SignedData is the parsed content when ContentType is OIDSignedData,
	// nil otherwise.
	SignedData *SignedData
	// Content is the DER encoding of the content field's value (the bytes
	// wrapped by the [0] EXPLICIT tag), always populated.
	Content []byte
}

// contentInfoASN1 is the wire shape of ContentInfo, used directly with
// encoding/asn1. The content field is captured as a RawValue rather than
// decoded further here because its shape depends on contentType.
type contentInfoASN1 struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"optional,explicit,tag:0"`
}

// ParseContentInfo decodes a catalog file's outer PKCS #7 ContentInfo from
// its DER encoding.
func ParseContentInfo(data []byte) (*ContentInfo, error) {
	var w contentInfoASN1
	rest, err := asn1.Unmarshal(data, &w)
	if err != nil {
		return nil, wrapErr("content info", err)
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("cat: content info: %d trailing byte(s)", len(rest))
	}
	ci := &ContentInfo{
		ContentType: w.ContentType,
		Content:     append([]byte(nil), w.Content.Bytes...),
	}
	if w.ContentType.Equal(OIDSignedData) {
		sd, err := ParseSignedData(ci.Content)
		if err != nil {
			return nil, wrapErr("signed data", err)
		}
		ci.SignedData = sd
	}
	return ci, nil
}

// AppendTo serializes the ContentInfo, appending its DER encoding to dst. If
// SignedData is non-nil it is re-serialized and takes precedence over
// Content; otherwise Content is emitted verbatim.
func (ci *ContentInfo) AppendTo(dst []byte) ([]byte, error) {
	content := ci.Content
	if ci.SignedData != nil {
		b, err := ci.SignedData.AppendTo(nil)
		if err != nil {
			return dst, wrapErr("signed data", err)
		}
		content = b
	}
	w := contentInfoASN1{
		ContentType: ci.ContentType,
		Content: asn1.RawValue{
			Class:      asn1.ClassContextSpecific,
			Tag:        0,
			IsCompound: true,
			Bytes:      content,
		},
	}
	b, err := asn1.Marshal(w)
	if err != nil {
		return dst, wrapErr("content info", err)
	}
	return append(dst, b...), nil
}

// SignedData is a PKCS #7 SignedData value (RFC 2315 §9.1):
//
//	SignedData ::= SEQUENCE {
//	    version          INTEGER,
//	    digestAlgorithms SET OF AlgorithmIdentifier,
//	    contentInfo      ContentInfo,     -- here: the encapsulated content
//	    certificates     [0] IMPLICIT SET OF ExtendedCertificateOrCertificate OPTIONAL,
//	    crls             [1] IMPLICIT SET OF CertificateRevocationList OPTIONAL,
//	    signerInfos      SET OF SignerInfo
//	}
//
// Catalog files set the encapsulated contentInfo's contentType to
// OIDCatalogList; CTL holds that content parsed into a
// CertificateTrustList. Certificates, CRLs, and SignerInfos are not
// interpreted at all (no X.509 parsing, no signature verification): each is
// preserved as an opaque, individually round-trippable DER blob, including
// its own tag, so serialization reproduces the original bytes exactly.
type SignedData struct {
	// Version is the SignedData version (1 for the version this package
	// targets; RFC 2315 §9.1 also defines version 0).
	Version int
	// DigestAlgorithms lists the digest algorithms used anywhere in this
	// SignedData (by the encapsulated content and/or the signers).
	DigestAlgorithms []pkix.AlgorithmIdentifier
	// ContentType identifies the shape of the encapsulated content. Catalog
	// files always use OIDCatalogList.
	ContentType asn1.ObjectIdentifier
	// CTL is the parsed encapsulated content when ContentType is
	// OIDCatalogList, nil otherwise.
	CTL *CertificateTrustList
	// Content is the DER encoding of the encapsulated content's value (the
	// bytes wrapped by contentInfo's [0] EXPLICIT tag), always populated.
	Content []byte
	// Certificates is the DER encoding of the whole "[0] IMPLICIT SET OF
	// ..." certificates field (including its own tag and length), or nil if
	// the field was absent. Opaque: no X.509 parsing is performed.
	Certificates []byte
	// CRLs is the DER encoding of the whole "[1] IMPLICIT SET OF ..." crls
	// field (including its own tag and length), or nil if the field was
	// absent. Opaque: no CRL parsing is performed.
	CRLs []byte
	// SignerInfos is the DER encoding of the whole "SET OF SignerInfo"
	// field (including its own tag and length). Opaque: no signature
	// verification is performed.
	SignerInfos []byte
}

// signedDataASN1 is the wire shape of SignedData.
type signedDataASN1 struct {
	Version          int
	DigestAlgorithms []pkix.AlgorithmIdentifier `asn1:"set"`
	EncapContentInfo contentInfoASN1
	Certificates     asn1.RawValue `asn1:"optional,tag:0"`
	CRLs             asn1.RawValue `asn1:"optional,tag:1"`
	SignerInfos      asn1.RawValue
}

// ParseSignedData decodes a bare SignedData value (the content of a
// ContentInfo whose contentType is OIDSignedData) from its DER encoding.
func ParseSignedData(data []byte) (*SignedData, error) {
	var w signedDataASN1
	rest, err := asn1.Unmarshal(data, &w)
	if err != nil {
		return nil, wrapErr("signed data", err)
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("cat: signed data: %d trailing byte(s)", len(rest))
	}
	sd := &SignedData{
		Version:          w.Version,
		DigestAlgorithms: w.DigestAlgorithms,
		ContentType:      w.EncapContentInfo.ContentType,
		Content:          append([]byte(nil), w.EncapContentInfo.Content.Bytes...),
		SignerInfos:      append([]byte(nil), w.SignerInfos.FullBytes...),
	}
	if len(w.Certificates.FullBytes) > 0 {
		sd.Certificates = append([]byte(nil), w.Certificates.FullBytes...)
	}
	if len(w.CRLs.FullBytes) > 0 {
		sd.CRLs = append([]byte(nil), w.CRLs.FullBytes...)
	}
	if sd.ContentType.Equal(OIDCatalogList) {
		ctl, err := ParseCTL(sd.Content)
		if err != nil {
			return nil, wrapErr("ctl", err)
		}
		sd.CTL = ctl
	}
	return sd, nil
}

// AppendTo serializes the SignedData, appending its DER encoding to dst. If
// CTL is non-nil it is re-serialized and takes precedence over Content;
// otherwise Content is emitted verbatim.
func (sd *SignedData) AppendTo(dst []byte) ([]byte, error) {
	content := sd.Content
	if sd.CTL != nil {
		b, err := sd.CTL.AppendTo(nil)
		if err != nil {
			return dst, wrapErr("ctl", err)
		}
		content = b
	}
	w := signedDataASN1{
		Version:          sd.Version,
		DigestAlgorithms: sd.DigestAlgorithms,
		EncapContentInfo: contentInfoASN1{
			ContentType: sd.ContentType,
			Content: asn1.RawValue{
				Class:      asn1.ClassContextSpecific,
				Tag:        0,
				IsCompound: true,
				Bytes:      content,
			},
		},
		SignerInfos: asn1.RawValue{FullBytes: sd.SignerInfos},
	}
	if sd.Certificates != nil {
		w.Certificates = asn1.RawValue{FullBytes: sd.Certificates}
	}
	if sd.CRLs != nil {
		w.CRLs = asn1.RawValue{FullBytes: sd.CRLs}
	}
	b, err := asn1.Marshal(w)
	if err != nil {
		return dst, wrapErr("signed data", err)
	}
	return append(dst, b...), nil
}
