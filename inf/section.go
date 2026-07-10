package inf

// Section is one "[Name]"-delimited section of an INF file: a name and an
// ordered list of entries. If Name is empty, this is the implicit preamble
// section holding any comment/blank lines before the first real header (see
// File.Sections).
type Section struct {
	// Name is the section name as declared between the brackets, with any
	// enclosing double quotes removed (see "Using String Tokens" and
	// "Section Names" in the general INF syntax rules). Comparisons
	// against Name elsewhere in this package are case-insensitive, per the
	// grammar.
	Name string
	// Comment is a trailing end-of-line comment on the "[Name]" header
	// line itself (text after ';', not including the ';'), or "" if none.
	Comment string
	// Entries holds every entry, comment-only line, and blank line in the
	// section, in on-disk order.
	Entries []Entry
}

// Entry is one logical line within a section: either a key/field entry, a
// bare (keyless) directive line, a standalone comment line, or a blank
// line. Exactly one of Blank, CommentOnly, or a "real" entry (HasKey and/or
// Fields) applies; see the field docs.
type Entry struct {
	// Blank marks this Entry as a blank line, preserved only to keep
	// Entries a faithful ordered record of the section's line structure.
	// Key, Fields, and Comment are unused when Blank is true.
	Blank bool
	// CommentOnly marks this Entry as a standalone comment line (a line
	// whose only content, besides leading whitespace, is a ';' comment).
	// Comment holds the comment text; Key and Fields are unused.
	CommentOnly bool
	// HasKey reports whether this entry had a "key = ..." form. When
	// false, the entry is a bare directive line consisting only of
	// comma-separated Fields (for example, a line inside a section
	// referenced by a CopyFiles directive, which lists one filename per
	// line with no key).
	HasKey bool
	// Key is the entry's key (the text before '='), when HasKey is true.
	Key string
	// Fields holds the comma-separated field values, in order, with
	// surrounding whitespace trimmed and (if present) one enclosing layer
	// of double quotes removed - doubled quotes ("") inside a quoted field
	// are collapsed to a single literal '"', per the INF quoting rules. A
	// field is never nil-vs-empty distinguished: an omitted field in a
	// list like "a,,c" is the empty string "".
	Fields []string
	// Comment is a trailing end-of-line comment on this entry (text after
	// ';', not including the ';'), or "" if none.
	Comment string
}

// Field returns the i'th field value and reports whether it exists. It is a
// convenience for the common case of reading a single positional field
// without bounds-checking Fields by hand.
func (e *Entry) Field(i int) (string, bool) {
	if i < 0 || i >= len(e.Fields) {
		return "", false
	}
	return e.Fields[i], true
}
