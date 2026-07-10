package inf

import "strings"

// AppendTo serializes f, appending its on-disk bytes to dst and returning
// the extended slice. See the package doc's "Round-trip contract" section
// for exactly what is preserved verbatim versus canonicalized.
func (f *File) AppendTo(dst []byte) []byte {
	var sb strings.Builder
	for _, s := range f.Sections {
		s.appendTo(&sb)
	}
	return append(dst, encodeFile(sb.String(), f.Unicode)...)
}

func (s *Section) appendTo(sb *strings.Builder) {
	if s.Name != "" {
		sb.WriteByte('[')
		sb.WriteString(quoteSectionName(s.Name))
		sb.WriteByte(']')
		writeComment(sb, s.Comment)
		sb.WriteString("\r\n")
	}
	for _, e := range s.Entries {
		e.appendTo(sb)
	}
}

func (e *Entry) appendTo(sb *strings.Builder) {
	switch {
	case e.Blank:
		sb.WriteString("\r\n")
		return
	case e.CommentOnly:
		sb.WriteByte(';')
		if e.Comment != "" {
			sb.WriteByte(' ')
			sb.WriteString(e.Comment)
		}
		sb.WriteString("\r\n")
		return
	}
	if e.HasKey {
		sb.WriteString(e.Key)
		sb.WriteString(" = ")
	}
	for i, field := range e.Fields {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(quoteField(field))
	}
	writeComment(sb, e.Comment)
	sb.WriteString("\r\n")
}

func writeComment(sb *strings.Builder, comment string) {
	if comment == "" {
		return
	}
	sb.WriteString("  ; ")
	sb.WriteString(comment)
}

// needsQuoting reports whether s must be wrapped in double quotes to
// round-trip through the INF grammar: it is empty, has leading/trailing
// whitespace, or contains a character with grammatical meaning ('"', ';',
// ',', or a line-continuation-triggering trailing '\', already covered by
// the trailing-whitespace/backslash check since a lone trailing '\' is
// itself the unquoted form of a continuation).
func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	if s != strings.TrimSpace(s) {
		return true
	}
	if strings.ContainsAny(s, `";,`) {
		return true
	}
	return strings.HasSuffix(s, `\`)
}

func quoteField(s string) string {
	if !needsQuoting(s) {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// quoteSectionName quotes a section name if needed to preserve characters
// that are otherwise significant or forbidden in an unquoted section name
// (see "Section Names" in the general INF syntax rules): brackets, a
// leading/trailing space, ';', '"', or a trailing '\'.
func quoteSectionName(s string) string {
	if s != strings.TrimSpace(s) || strings.ContainsAny(s, `[]";`) || strings.HasSuffix(s, `\`) {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}
