package wim

import (
	"path"
	"strings"
)

// MatchName reports whether name matches a DOS-style glob pattern over a
// single path component (no separators), case-insensitively (as Windows
// names are). '*' matches any sequence of characters and '?' matches any
// single character; '[...]' character classes are also supported, since they
// come for free from the underlying matcher.
//
// This wraps the standard library's path.Match rather than reimplementing
// glob matching: path.Match already implements POSIX shell pattern syntax,
// which is exactly '*'/'?'/'[...]' matching over a slash-free string here
// (pattern and name are folded to the same case first, and neither is
// expected to contain '/', so path.Match's separator handling never comes
// into play). Only a single name component is supported - full path-glob
// semantics across '/'-separated segments is out of scope, since the only
// need here is filtering names like driver-folder entries ("prn*") or file
// extensions ("*.inf").
//
// A malformed pattern (an unterminated '[' class) makes path.Match report
// ErrBadPattern; MatchName treats that as no match rather than surfacing the
// error, since callers filtering a list of names have no useful way to react
// to a bad pattern other than skipping it.
func MatchName(pattern, name string) bool {
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(name))
	if err != nil {
		return false
	}
	return ok
}
