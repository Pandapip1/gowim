package cat

import (
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"math/big"
	"time"
)

// CertificateTrustList is the Microsoft CTL (Certificate Trust List)
// structure embedded as a catalog file's encapsulated content, under
// OIDCatalogList. Based on MS-CAESO (cited by ralphje/signify's
// signify/asn1/ctl.py, https://github.com/ralphje/signify):
//
//	CertificateTrustList ::= SEQUENCE {
//	    version           CTLVersion DEFAULT v1,       -- INTEGER {v1(0)}
//	    subjectUsage      SubjectUsage,                 -- SEQUENCE OF OID
//	    listIdentifier    ListIdentifier OPTIONAL,       -- OCTET STRING
//	    sequenceNumber    HUGEINTEGER OPTIONAL,          -- INTEGER
//	    ctlThisUpdate     ChoiceOfTime,
//	    ctlNextUpdate     ChoiceOfTime OPTIONAL,
//	    subjectAlgorithm  AlgorithmIdentifier,
//	    trustedSubjects   TrustedSubjects OPTIONAL,      -- SEQUENCE OF TrustedSubject
//	    ctlExtensions     [0] EXPLICIT Extensions OPTIONAL
//	}
//
// For a catalog file, each TrustedSubject (Members) describes one file
// covered by the catalog: its Tag (subjectIdentifier) and Attributes carry,
// among other things, the CAT_NAMEVALUE_OBJID name/value pairs (see
// CatalogMember.NameValues) and the per-file digest (see
// CatalogMember.Digest).
type CertificateTrustList struct {
	// Version is the CTL structure version; 0 conventionally means v1 and is
	// the DER-encoded default (the field is omitted on the wire when 0).
	Version int `asn1:"optional,default:0"`
	// SubjectUsage lists the enhanced-key-usage OIDs this CTL applies to.
	// For a catalog file this is normally just OIDCatalogList.
	SubjectUsage []asn1.ObjectIdentifier
	// ListIdentifier is an opaque identifier for this CTL, if present.
	ListIdentifier []byte `asn1:"optional"`
	// SequenceNumber is a monotonically increasing sequence number for this
	// CTL, if present.
	SequenceNumber *big.Int `asn1:"optional"`
	// ThisUpdate is the CTL's effective/creation time.
	ThisUpdate time.Time
	// NextUpdate is the CTL's expiry time, if present (the zero Time if
	// absent).
	NextUpdate time.Time `asn1:"optional"`
	// SubjectAlgorithm is the digest algorithm used to compute each member's
	// hash. Older (V1) catalogs instead use OIDCatalogListMember here as a
	// marker; see that constant's doc comment.
	SubjectAlgorithm pkix.AlgorithmIdentifier
	// Members lists the files (or other trusted subjects) this CTL covers.
	Members []CatalogMember `asn1:"optional"`
	// Extensions is the DER encoding of the (opaque, not further
	// interpreted) inner Extensions SEQUENCE wrapped by the CTL's "[0]
	// EXPLICIT Extensions OPTIONAL" field, or nil if absent.
	Extensions asn1.RawValue `asn1:"optional,explicit,tag:0"`
}

// CatalogMember is one TrustedSubject entry of a CertificateTrustList:
//
//	TrustedSubject ::= SEQUENCE {
//	    subjectIdentifier SubjectIdentifier,  -- OCTET STRING
//	    subjectAttributes Attributes OPTIONAL -- SET OF Attribute
//	}
type CatalogMember struct {
	// Tag identifies the member, conventionally derived from a hash of the
	// member's name (subjectIdentifier on the wire).
	Tag []byte
	// Attributes carries the member's PKCS #9-style attributes, e.g. the
	// CAT_NAMEVALUE_OBJID name/value pairs and the SPC_INDIRECT_DATA_OBJID
	// per-file digest.
	Attributes []Attribute `asn1:"optional,set"`
}

// Attribute is a PKCS #9-style Attribute (as used throughout PKCS #7/CMS for
// signed/subject attributes):
//
//	Attribute ::= SEQUENCE {
//	    type   OBJECT IDENTIFIER,
//	    values SET OF ANY DEFINED BY type
//	}
//
// Values are preserved as opaque, individually round-trippable DER blobs;
// see CatalogMember.NameValues and CatalogMember.Digest for the two shapes
// this package interprets.
type Attribute struct {
	Type   asn1.ObjectIdentifier
	Values []asn1.RawValue `asn1:"set"`
}

// ParseCTL decodes a CertificateTrustList from its DER encoding (the content
// of a catalog's encapsulated ContentInfo).
func ParseCTL(data []byte) (*CertificateTrustList, error) {
	var ctl CertificateTrustList
	rest, err := asn1.Unmarshal(data, &ctl)
	if err != nil {
		return nil, wrapErr("ctl", err)
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("cat: ctl: %d trailing byte(s)", len(rest))
	}
	return &ctl, nil
}

// AppendTo serializes the CertificateTrustList, appending its DER encoding
// to dst.
func (c *CertificateTrustList) AppendTo(dst []byte) ([]byte, error) {
	b, err := asn1.Marshal(*c)
	if err != nil {
		return dst, wrapErr("ctl", err)
	}
	return append(dst, b...), nil
}

// nameValueASN1 is the wire shape of a CAT_NAMEVALUE_OBJID entry, based on
// the CAT_NAMEVALUE struct in wintrust.h (see
// https://microsoft.github.io/windows-docs-rs/doc/windows/Win32/Security/WinTrust/struct.CAT_NAMEVALUE.html,
// cross-checked against signify's asn1/ctl.py NameValue):
//
//	NameValue ::= SEQUENCE {
//	    refname    BMPSTRING,
//	    typeaction INTEGER,
//	    value      OCTETSTRING   -- conventionally UTF-16LE text
//	}
type nameValueASN1 struct {
	RefName    asn1.RawValue // BMPString (UCS-2, big-endian)
	TypeAction int32
	Value      []byte
}

// NameValue is a decoded CAT_NAMEVALUE_OBJID entry: a Name=Value pair such as
// File=<filename>, OS=<supported OS list>, or OSAttr=<OS attribute string>,
// as commonly carried by driver catalogs.
type NameValue struct {
	// Name is the entry's tag/key (refname on the wire), e.g. "File".
	Name string
	// Flags is the typeaction field; this package does not interpret its
	// bits.
	Flags int32
	// Value is the entry's value, decoded from its UTF-16LE encoding with
	// any trailing NUL trimmed.
	Value string
}

// NameValues decodes the member's CAT_NAMEVALUE_OBJID attribute (if any)
// into Name=Value pairs. It returns a nil slice (no error) if the member has
// no such attribute.
func (m *CatalogMember) NameValues() ([]NameValue, error) {
	for _, a := range m.Attributes {
		if !a.Type.Equal(OIDCatNameValue) {
			continue
		}
		out := make([]NameValue, 0, len(a.Values))
		for _, v := range a.Values {
			var raw nameValueASN1
			if _, err := asn1.Unmarshal(v.FullBytes, &raw); err != nil {
				return nil, wrapErr("name value", err)
			}
			out = append(out, NameValue{
				Name:  bmpStringToUTF8(raw.RefName.Bytes),
				Flags: raw.TypeAction,
				Value: utf16leToString(raw.Value),
			})
		}
		return out, nil
	}
	return nil, nil
}

// File returns the value of the member's "File" CAT_NAMEVALUE entry, the
// name of the driver-package file this member covers. ok is false if the
// member has no such entry.
func (m *CatalogMember) File() (name string, ok bool, err error) {
	nvs, err := m.NameValues()
	if err != nil {
		return "", false, err
	}
	for _, nv := range nvs {
		if nv.Name == "File" {
			return nv.Value, true, nil
		}
	}
	return "", false, nil
}

// digestInfoASN1 mirrors RFC 2315's DigestInfo, used inside
// spcIndirectDataContentASN1 to carry a member file's hash.
type digestInfoASN1 struct {
	DigestAlgorithm pkix.AlgorithmIdentifier
	Digest          []byte
}

// spcAttributeTypeAndOptionalValueASN1 mirrors SpcAttributeTypeAndOptionalValue
// from the Authenticode "Windows Authenticode Portable Executable Signature
// Format" spec:
//
//	SpcAttributeTypeAndOptionalValue ::= SEQUENCE {
//	    type  OBJECT IDENTIFIER,
//	    value ANY OPTIONAL
//	}
type spcAttributeTypeAndOptionalValueASN1 struct {
	Type  asn1.ObjectIdentifier
	Value asn1.RawValue `asn1:"optional"`
}

// spcIndirectDataContentASN1 mirrors SpcIndirectDataContent from the same
// spec, which carries a member file's digest algorithm and hash inside the
// SPC_INDIRECT_DATA_OBJID attribute value:
//
//	SpcIndirectDataContent ::= SEQUENCE {
//	    data          SpcAttributeTypeAndOptionalValue,
//	    messageDigest DigestInfo
//	}
type spcIndirectDataContentASN1 struct {
	Data          spcAttributeTypeAndOptionalValueASN1
	MessageDigest digestInfoASN1
}

// Digest returns the digest algorithm OID and hash bytes carried by the
// member's SPC_INDIRECT_DATA_OBJID attribute -- the per-file hash this
// catalog member vouches for. ok is false if the member has no such
// attribute.
func (m *CatalogMember) Digest() (algorithm asn1.ObjectIdentifier, hash []byte, ok bool, err error) {
	for _, a := range m.Attributes {
		if !a.Type.Equal(OIDSpcIndirectDataContent) {
			continue
		}
		if len(a.Values) == 0 {
			continue
		}
		var idc spcIndirectDataContentASN1
		if _, err := asn1.Unmarshal(a.Values[0].FullBytes, &idc); err != nil {
			return nil, nil, false, wrapErr("spc indirect data content", err)
		}
		return idc.MessageDigest.DigestAlgorithm.Algorithm, idc.MessageDigest.Digest, true, nil
	}
	return nil, nil, false, nil
}
