package iso

import (
	"strings"
	"unicode/utf16"
)

// InterchangeLevel selects the ECMA-119 clause 10 level of interchange,
// which is what actually constrains identifier lengths and whether a file
// may be split across several File Sections.
//
// ECMA-119 10.1 (Level 1): each file shall consist of only one File
// Section; a File Name shall not contain more than eight d-characters; a
// File Name Extension shall not contain more than three; a Directory
// Identifier shall not contain more than eight. This is the classic "8.3"
// restriction.
//
// ECMA-119 10.2 (Level 2): each file shall consist of only one File
// Section, and nothing else. Identifiers may therefore run to the 7.5.1
// maximum of 30 characters across name plus extension, and Directory
// Identifiers to the 7.6.3 maximum.
//
// ECMA-119 10.3 (Level 3): "no restrictions shall apply". This is the only
// level at which a file may be recorded as more than one File Section, and
// hence the only level at which a file larger than 4 GiB - 1 can be
// represented in ISO 9660 at all (see 6.5.1 and the Data Length field of
// 9.1.4).
//
// Note that genisoimage's "-iso-level 4" is NOT an ECMA-119 level. It is
// genisoimage's own shorthand for ISO 9660:1999, i.e. the Enhanced Volume
// Descriptor of ECMA-119 8.5 with File Structure Version 2 (8.4.30), which
// relaxes identifiers to 207 characters and drops version numbers. That is
// visible in cdrkit-1.1.11/genisoimage/genisoimage.c, the OPTION_ISO_LEVEL
// case, whose "case 4:" arm is commented "This is ISO-9660:1988 [sic]
// (ISO-9660 version 2)" and sets iso9660_namelen = MAX_ISONAME_V2 (207),
// omit_version_number, relaxed_filenames, allow_lowercase and friends. This
// package does not implement that yet; see the package doc.
type InterchangeLevel int

const (
	// Level1 restricts identifiers to 8.3 and forbids multi-extent files
	// (ECMA-119 10.1).
	Level1 InterchangeLevel = 1
	// Level2 allows long identifiers but still forbids multi-extent files
	// (ECMA-119 10.2).
	Level2 InterchangeLevel = 2
	// Level3 applies no restrictions and is required for files recorded as
	// more than one File Section (ECMA-119 10.3, 6.5.1).
	Level3 InterchangeLevel = 3
)

// maxFileNameTotal is the ECMA-119 7.5.1 limit for a directory hierarchy
// identified by a Primary Volume Descriptor: "The sum of the following
// shall not exceed 30: ... the length of the File Name; ... the length of
// the File Name Extension."
const maxFileNameTotal = 30

// maxDirIDLen is the ECMA-119 7.6.3 limit on a Directory Identifier for a
// hierarchy identified by a Primary Volume Descriptor. 7.6.3 gives 31; the
// value is repeated in cdrkit-1.1.11/genisoimage/iso9660.h as
// LEN_ISONAME 31.
const maxDirIDLen = 31

// isDChar reports whether c is a d-character.
//
// ECMA-119 7.4.1: the 37 d-characters are the ECMA-6 International
// Reference Version positions 3/0 to 3/9 (the digits 0-9), 4/1 to 5/10
// (the capitals A-Z) and 5/15 (LOW LINE, i.e. underscore). Note what is
// absent: lowercase, space, and every punctuation mark other than
// underscore.
func isDChar(c byte) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c == '_':
		return true
	}
	return false
}

// isAChar reports whether c is an a-character.
//
// ECMA-119 7.4.1: the 57 a-characters are the ECMA-6 IRV positions 2/0 to
// 2/2, 2/5 to 2/15, 3/0 to 3/15, 4/1 to 4/15, 5/0 to 5/10 and 5/15. In
// byte terms that is 0x20-0x22, 0x25-0x2F, 0x30-0x3F, 0x41-0x4F,
// 0x50-0x5A and 0x5F. The gaps are deliberate: 0x23 '#', 0x24 '$',
// 0x40 '@', 0x5B-0x5E '[' '\' ']' '^' and all lowercase are excluded.
func isAChar(c byte) bool {
	switch {
	case c >= 0x20 && c <= 0x22:
		return true
	case c >= 0x25 && c <= 0x3F:
		return true
	case c >= 0x41 && c <= 0x5A:
		return true
	case c == 0x5F:
		return true
	}
	return false
}

// sanitizeAChars reduces s to a-characters for the identifier fields of the
// Primary Volume Descriptor (System Identifier 8.4.5, Volume Set Identifier
// 8.4.19, Publisher 8.4.20, Data Preparer 8.4.21, Application 8.4.22).
//
// Lowercase is upper-cased rather than dropped, since that is the intent of
// nearly every caller; anything still not an a-character becomes an
// underscore, which is itself an a-character (position 5/15). Losing the
// original character is acceptable here because these fields are purely
// descriptive.
func sanitizeAChars(s string) string {
	b := []byte(strings.ToUpper(s))
	for i, c := range b {
		if !isAChar(c) {
			b[i] = '_'
		}
	}
	return string(b)
}

// sanitizeDChars reduces s to d-characters, for the Volume Identifier
// (ECMA-119 8.4.6, "d-characters") and as the first step of file and
// directory identifier mangling.
func sanitizeDChars(s string) string {
	b := []byte(strings.ToUpper(s))
	for i, c := range b {
		if !isDChar(c) {
			b[i] = '_'
		}
	}
	return string(b)
}

// mangleFileName converts a host filename into an ECMA-119 File Identifier
// *without* the version-number suffix, which is appended separately at
// record-emission time.
//
// ECMA-119 7.5.1 gives the File Identifier as
//
//	File Name, SEPARATOR 1, File Name Extension, SEPARATOR 2, File Version Number
//
// where SEPARATOR 1 is '.' and SEPARATOR 2 is ';' (7.4.3.1), and requires
// that at least one of File Name and File Name Extension be non-empty. The
// split point chosen here is the last '.' in the host name, which is what
// users expect; a leading dot is not treated as an extension separator
// because that would leave an empty File Name for names like ".config".
//
// At Level1 the name is truncated to 8 d-characters and the extension to 3
// (ECMA-119 10.1). At Level2 and Level3 the two are truncated so that their
// sum does not exceed 30 (7.5.1), preferring to keep the extension intact
// since it usually carries the type.
func mangleFileName(name string, level InterchangeLevel) string {
	base, ext := splitExt(name)
	base = sanitizeDChars(base)
	ext = sanitizeDChars(ext)

	if level == Level1 {
		base = truncate(base, 8)
		ext = truncate(ext, 3)
	} else {
		ext = truncate(ext, maxFileNameTotal-1)
		if len(base)+len(ext) > maxFileNameTotal {
			base = truncate(base, maxFileNameTotal-len(ext))
		}
	}

	// 7.5.1 requires at least one of the two components to be non-empty.
	// A host name consisting solely of separator characters can sanitize
	// down to nothing, so substitute a placeholder rather than emit an
	// illegal identifier.
	if base == "" && ext == "" {
		base = "_"
	}
	return base + "." + ext
}

// mangleDirName converts a host directory name into an ECMA-119 Directory
// Identifier (7.6). Unlike a File Identifier a Directory Identifier has no
// extension and no version number: 7.6.1 makes it a plain sequence of
// d-characters. The '.' is therefore not special and is folded to '_' along
// with everything else that is not a d-character.
func mangleDirName(name string, level InterchangeLevel) string {
	d := sanitizeDChars(name)
	if level == Level1 {
		d = truncate(d, 8)
	} else {
		d = truncate(d, maxDirIDLen)
	}
	if d == "" {
		d = "_"
	}
	return d
}

// splitExt splits a host filename at the last '.' into base and extension.
// A leading dot is not a separator, so ".bashrc" yields base ".bashrc" and
// an empty extension (which sanitizeDChars then turns into "_BASHRC").
func splitExt(name string) (string, string) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return name, ""
	}
	return name[:i], name[i+1:]
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// dedupe makes want unique within used by overwriting its tail with a
// decimal counter, and records the result in used.
//
// ECMA-119 does not permit two Directory Records in the same directory to
// have the same File Identifier including version number, and mangling is
// lossy enough (case folding, character substitution, truncation) that
// collisions are routine: "Foo.txt" and "foo.txt" mangle identically. The
// counter is written over the *end* of the name component so that the
// result still respects the length limit the caller already applied.
func dedupe(used map[string]bool, want string, isDir bool, level InterchangeLevel) string {
	if !used[want] {
		used[want] = true
		return want
	}
	limit := maxDirIDLen
	if level == Level1 {
		limit = 8
	}
	base, ext := want, ""
	if !isDir {
		if i := strings.LastIndexByte(want, '.'); i >= 0 {
			base, ext = want[:i], want[i+1:]
		}
		limit = maxFileNameTotal - len(ext)
		if level == Level1 {
			limit = 8
		}
	}
	for n := 1; ; n++ {
		suffix := itoa(n)
		keep := limit - len(suffix)
		if keep < 0 {
			keep = 0
		}
		cand := truncate(base, keep) + suffix
		if !isDir {
			cand += "." + ext
		}
		if !used[cand] {
			used[cand] = true
			return cand
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// jolietMaxNameLen is the maximum length, in UCS-2 code units, of a Joliet
// file or directory identifier.
//
// Joliet was never standardized by Ecma or ISO; it is a Microsoft-authored
// extension, and this package's source for its on-disk rules is the real
// cross-checkable implementation cited throughout this file: cdrkit 1.1.11's
// genisoimage (already cached from earlier phases at
// /tmp/claude/repos/cdrkit-1.1.11/genisoimage/). genisoimage.h defines
// `JMAX 64 /* maximum Joliet file name length (spec) */` and joliet.c's
// joliet_strlen clamps every converted name to `2*jlen` bytes, i.e. jlen
// UCS-2 code units, with jlen defaulting to JMAX. genisoimage's
// out-of-spec `-joliet-long` raises this to `JLONGMAX 103`; this package
// does not offer that, only the in-spec 64.
const jolietMaxNameLen = 64

// mangleJolietName converts a host name into a Joliet identifier: the name
// re-encoded as UCS-2 (recorded big-endian, per joliet.go's writer), with
// illegal characters folded to '_' and the result truncated to
// jolietMaxNameLen code units.
//
// Unlike mangleFileName/mangleDirName, no name/extension split, case
// folding, or version-number suffix is applied: joliet.c stores the
// original host name verbatim (`s_entry->name = strdup(short_name)` in
// cdrkit-1.1.11/genisoimage/tree.c, where at that point `short_name` is
// still the pre-8.3-mangling name) and it is this original name, not the
// mangled ECMA-119 identifier, that convert_to_unicode later encodes for
// the Joliet tree. Directories and files are treated identically because
// joliet.c's generate_one_joliet_directory and get_joliet_vol_desc route
// both through the very same convert_to_unicode call.
//
// The illegal-character set is documented in joliet.c's own file comment
// and enforced in convert_to_unicode's switch statement:
//
//	Characters (00)(00) through (00)(1f) (control chars)
//	(00)(2a) '*'
//	(00)(2f) '/'
//	(00)(3a) ':'
//	(00)(3b) ';'
//	(00)(3f) '?'
//	(00)(5c) '\'
//
// convert_to_unicode additionally folds (00)(7f) (DEL) to '_'
// (`if (uc <= 0x1f || uc == 0x7f) uc = '\0'`, then the switch maps the
// sentinel 0 to '_'), which the file comment omits but the code does not.
func mangleJolietName(name string) []uint16 {
	units := utf16.Encode([]rune(name))
	if len(units) > jolietMaxNameLen {
		units = units[:jolietMaxNameLen]
	}
	out := make([]uint16, len(units))
	for i, u := range units {
		out[i] = jolietSanitizeUnit(u)
	}
	if len(out) == 0 {
		// Every mangleFileName/mangleDirName path also refuses to produce an
		// empty identifier (ECMA-119 7.5.1 requires at least one non-empty
		// component); Joliet has no such normative text since it was never
		// standardized, but an empty File/Directory Identifier is not a
		// legal Directory Record (LEN_FI would be 0), so the same
		// placeholder is used here for consistency.
		out = []uint16{'_'}
	}
	return out
}

// jolietSanitizeUnit folds one UCS-2 code unit to '_' if it is outside the
// legal set documented on mangleJolietName.
func jolietSanitizeUnit(u uint16) uint16 {
	switch {
	case u <= 0x1f, u == 0x7f:
		return '_'
	case u == '*', u == '/', u == ':', u == ';', u == '?', u == '\\':
		return '_'
	}
	return u
}

// jolietBytes encodes UCS-2 code units as big-endian bytes ("Unicode strings
// are always encoded in big-endian format", joliet.c's file comment).
func jolietBytes(units []uint16) []byte {
	b := make([]byte, len(units)*2)
	for i, u := range units {
		b[2*i] = byte(u >> 8)
		b[2*i+1] = byte(u)
	}
	return b
}

// jolietDedupe makes want unique within used by overwriting its tail with a
// decimal counter, mirroring dedupe's role for ECMA-119 identifiers.
//
// genisoimage does not do this: joliet_compare_dirs treats two siblings
// that collide after Joliet mangling as a fatal error ("Error: %s and %s
// have the same Joliet name", cdrkit-1.1.11/genisoimage/joliet.c). Since a
// gowim caller's host names are already guaranteed unique within a
// directory (AddFile/AddTree only ever see one entry per host name) and a
// collision can only arise from truncation or illegal-character folding —
// e.g. two names differing only past the 64th code unit — resolving it
// deterministically is more useful to a caller than aborting the whole
// build, so this package deviates from genisoimage here.
func jolietDedupe(used map[string]bool, want []uint16) []uint16 {
	key := string(jolietBytes(want))
	if !used[key] {
		used[key] = true
		return want
	}
	for n := 1; ; n++ {
		suffix := utf16.Encode([]rune(itoa(n)))
		keep := jolietMaxNameLen - len(suffix)
		if keep < 0 {
			keep = 0
		}
		if keep > len(want) {
			keep = len(want)
		}
		cand := append(append([]uint16{}, want[:keep]...), suffix...)
		k := string(jolietBytes(cand))
		if !used[k] {
			used[k] = true
			return cand
		}
	}
}

// compareFileIdentifiers implements the ordering of ECMA-119 9.3, "Order of
// Directory Records", for two identifiers given as (name, extension,
// version) triples. It returns a negative number if a sorts before b.
//
// 9.3 orders by, in descending significance:
//
//   - ascending File Name, where a shorter name is compared as if padded on
//     the right with FILLER (0x20) to equal length;
//   - ascending File Name Extension, padded the same way;
//   - *descending* File Version Number, a shorter version being compared as
//     if padded on the *left* with (30), i.e. the digit zero;
//   - descending Associated File bit.
//
// The FILLER padding rule is not the same as a plain byte comparison: 0x20
// sorts below every d-character, so "AB" sorts before "ABC" — which a plain
// prefix comparison also gives — but crucially the rule makes the ordering
// well defined against identifiers containing SEPARATOR characters. This
// package always compares the components separately, which is exactly what
// the clause describes.
//
// The Associated File bit is not used by this package (no Associated Files
// are ever produced), so that final criterion never fires.
func compareFileIdentifiers(aName, aExt string, aVer uint16, bName, bExt string, bVer uint16) int {
	if c := comparePadded(aName, bName, filler); c != 0 {
		return c
	}
	if c := comparePadded(aExt, bExt, filler); c != 0 {
		return c
	}
	// Descending version number.
	switch {
	case aVer > bVer:
		return -1
	case aVer < bVer:
		return 1
	}
	return 0
}

// comparePadded compares a and b as if the shorter were padded on the right
// with pad, per the valuation rules of ECMA-119 9.3 and 6.9.1.
func comparePadded(a, b string, pad byte) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		ca, cb := pad, pad
		if i < len(a) {
			ca = a[i]
		}
		if i < len(b) {
			cb = b[i]
		}
		if ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
	}
	return 0
}
