package cat

import "encoding/asn1"

// Named OID constants used by catalog files. Each is cited to the source
// that documents it; several are cross-checked against the reference
// implementation in ralphje/signify (signify/asn1/ctl.py and
// signify/asn1/spc.py, https://github.com/ralphje/signify), which in turn
// cites Microsoft's MS-CAESO protocol document and the Authenticode PE
// signature format spec.
var (
	// OIDSignedData is PKCS #7 SignedData's contentType, "signedData"
	// (RFC 2315 §14, id-signedData).
	OIDSignedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}

	// OIDCatalogList is szOID_CATALOG_LIST, the encapContentInfo contentType
	// that identifies a catalog file's embedded content as a Microsoft
	// Certificate Trust List (CTL). See Microsoft's "Object IDs associated
	// with Microsoft cryptography" (https://mskb.pkisolutions.com/kb/287547)
	// and signify's SubjectUsageObjectIdentifier map (microsoft_catalog_list).
	OIDCatalogList = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 12, 1, 1}

	// OIDCatalogListMember is szOID_CATALOG_LIST_MEMBER. Older (V1) catalogs
	// use this OID as the CTL's subjectAlgorithm to signal that member
	// hashes are computed the "catalog list member" way rather than with a
	// plain digest algorithm; see signify's
	// DigestAlgorithmId._map["1.3.6.1.4.1.311.12.1.2"] and
	// https://mskb.pkisolutions.com/kb/287547.
	OIDCatalogListMember = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 12, 1, 2}

	// OIDCatNameValue is CAT_NAMEVALUE_OBJID, the subject-attribute type
	// carrying a member's Name=Value pairs (File, OS, OSAttr, ...). See
	// signify's SubjectAttributeType._map["1.3.6.1.4.1.311.12.2.1"]
	// (microsoft_cat_namevalue) and the CAT_NAMEVALUE struct in wintrust.h.
	OIDCatNameValue = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 12, 2, 1}

	// OIDCatMemberInfo is CAT_MEMBERINFO_OBJID, a subject attribute carrying
	// a CAT_MEMBERINFO {subguid, certversion} pair. See signify's
	// SubjectAttributeType._map["1.3.6.1.4.1.311.12.2.2"]
	// (microsoft_cat_memberinfo).
	OIDCatMemberInfo = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 12, 2, 2}

	// OIDCatMemberInfo2 is CAT_MEMBERINFO2_OBJID, a newer variant of
	// OIDCatMemberInfo. See signify's
	// SubjectAttributeType._map["1.3.6.1.4.1.311.12.2.3"]
	// (microsoft_cat_memberinfo2).
	OIDCatMemberInfo2 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 12, 2, 3}

	// OIDSpcIndirectDataContent is SPC_INDIRECT_DATA_OBJID, the subject
	// attribute type that carries a member file's digest algorithm and hash
	// (an SpcIndirectDataContent value, itself defined by the "Windows
	// Authenticode Portable Executable Signature Format" spec). See
	// signify's SubjectAttributeType._map["1.3.6.1.4.1.311.2.1.4"]
	// (microsoft_spc_indirect_data_content) and asn1/spc.py's
	// SpcIndirectDataContent.
	OIDSpcIndirectDataContent = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 2, 1, 4}

	// OIDSHA1 is id-sha1, a common digest algorithm identifier seen in
	// catalog files (RFC 3279 §2.2.1 / RFC 8017 Appendix B.1).
	OIDSHA1 = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}

	// OIDSHA256 is id-sha256, the digest algorithm used by newer (V2)
	// catalogs (NIST OID arc, RFC 8017 Appendix B.1).
	OIDSHA256 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
)
