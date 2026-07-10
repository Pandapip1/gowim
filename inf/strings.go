package inf

import "strings"

// Lookup resolves a %strkey% token to its value, per the [Strings] /
// [Strings.LanguageID] mechanism (see "INF Strings Section",
// https://learn.microsoft.com/windows-hardware/drivers/install/inf-strings-section).
// strkey is given without the surrounding '%' characters.
//
// If langID is non-empty, the "Strings.langID" section is consulted first
// (langID is the 4-hex-digit LANGID suffix, e.g. "0407" for German); the
// undecorated [Strings] section is used as a fallback, or directly when
// langID is empty. This package does not implement the full LANGID
// fallback chain described in the documentation (exact match, then
// SUBLANG_NEUTRAL, then any sublanguage in the same primary language) -
// callers that need it can walk File.SectionsNamed("Strings." + ...)
// themselves, since every locale section is preserved structurally.
func (f *File) Lookup(strkey, langID string) (string, bool) {
	if langID != "" {
		if v, ok := lookupIn(f, "Strings."+langID, strkey); ok {
			return v, true
		}
	}
	return lookupIn(f, "Strings", strkey)
}

func lookupIn(f *File, sectionName, strkey string) (string, bool) {
	sec, ok := f.Section(sectionName)
	if !ok {
		return "", false
	}
	for _, e := range sec.Entries {
		if e.HasKey && equalFold(e.Key, strkey) && len(e.Fields) > 0 {
			return e.Fields[0], true
		}
	}
	return "", false
}

// Expand replaces every %strkey% token in s with its looked-up value (via
// Lookup with the given langID) and unescapes %% to a literal '%', per the
// escaping rule in the general INF syntax rules ("In order to include a
// percent (%) character ..., escape the percent character with another
// percent character"). A %strkey% token with no matching [Strings] entry is
// left in the output unexpanded (Windows itself leaves undefined tokens
// unexpanded rather than failing).
func (f *File) Expand(s, langID string) string {
	var sb strings.Builder
	for {
		i := strings.IndexByte(s, '%')
		if i < 0 {
			sb.WriteString(s)
			break
		}
		sb.WriteString(s[:i])
		rest := s[i+1:]
		if strings.HasPrefix(rest, "%") {
			sb.WriteByte('%')
			s = rest[1:]
			continue
		}
		j := strings.IndexByte(rest, '%')
		if j < 0 {
			// Unterminated '%': not a token, emit literally.
			sb.WriteByte('%')
			s = rest
			continue
		}
		token := rest[:j]
		if val, ok := f.Lookup(token, langID); ok {
			sb.WriteString(val)
		} else {
			sb.WriteByte('%')
			sb.WriteString(token)
			sb.WriteByte('%')
		}
		s = rest[j+1:]
	}
	return sb.String()
}
