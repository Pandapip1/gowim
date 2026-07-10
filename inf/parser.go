package inf

import (
	"fmt"
	"strings"
)

// ParseFile parses the on-disk bytes of an INF file: a BOM-prefixed
// UTF-16LE document, or a non-Unicode (ANSI/OEM/UTF-8) document, laid out as
// described in the package doc.
func ParseFile(data []byte) (*File, error) {
	text, unicode := decodeFile(data)
	lines, err := joinContinuations(splitPhysicalLines(text))
	if err != nil {
		return nil, wrapErr("parse", err)
	}

	f := &File{Unicode: unicode}
	// cur accumulates the section currently being built. Before the first
	// "[Section]" header, it is the implicit, unnamed preamble section
	// (see File.Sections); it is flushed into f.Sections (if non-empty, in
	// the preamble's case) whenever a new header is seen and at EOF.
	cur := Section{}
	haveHeader := false
	flush := func() {
		if haveHeader || len(cur.Entries) > 0 {
			f.Sections = append(f.Sections, cur)
		}
	}

	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln.code)
		switch {
		case trimmed == "" && ln.comment == "":
			cur.Entries = append(cur.Entries, Entry{Blank: true})
		case trimmed == "":
			cur.Entries = append(cur.Entries, Entry{CommentOnly: true, Comment: ln.comment})
		case strings.HasPrefix(trimmed, "["):
			name, err := parseSectionHeader(trimmed)
			if err != nil {
				return nil, wrapErr("parse", err)
			}
			flush()
			cur = Section{Name: name, Comment: ln.comment}
			haveHeader = true
		default:
			cur.Entries = append(cur.Entries, parseEntry(trimmed, ln.comment))
		}
	}
	flush()
	return f, nil
}

// logicalLine is one joined (continuation-resolved) logical line: the code
// portion (everything but the comment) and its trailing comment text, if
// any (without the leading ';').
type logicalLine struct {
	code    string
	comment string
}

// splitPhysicalLines splits text on bare "\n" or "\r\n" line endings.
func splitPhysicalLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// joinContinuations resolves ';' comments and trailing '\' line
// continuations (outside quoted strings) into logical lines, per "Line
// Format, Continuation, and Comments" in the general INF syntax rules.
func joinContinuations(physical []string) ([]logicalLine, error) {
	var out []logicalLine
	for i := 0; i < len(physical); i++ {
		code, comment := splitComment(physical[i])
		var comments []string
		if comment != "" {
			comments = append(comments, comment)
		}
		for {
			codeTrimmedRight := strings.TrimRight(code, " \t")
			if !endsInUnquotedBackslash(codeTrimmedRight) {
				code = codeTrimmedRight
				break
			}
			code = codeTrimmedRight[:len(codeTrimmedRight)-1]
			i++
			if i >= len(physical) {
				return nil, fmt.Errorf("line continuation at end of file")
			}
			nextCode, nextComment := splitComment(physical[i])
			code += nextCode
			if nextComment != "" {
				comments = append(comments, nextComment)
			}
		}
		out = append(out, logicalLine{code: code, comment: strings.Join(comments, " ")})
	}
	return out, nil
}

// splitComment splits a physical line into its code and trailing comment
// (text after the first unquoted ';', trimmed of leading whitespace),
// per the rule that ';' begins a comment unless it appears inside a
// double-quoted string.
func splitComment(line string) (code, comment string) {
	idx, _, found := scanOutsideQuotes(line, ";")
	if !found {
		return line, ""
	}
	return line[:idx], strings.TrimSpace(line[idx+1:])
}

// endsInUnquotedBackslash reports whether s ends with a '\' that is outside
// any double-quoted region, i.e. is a line continuator rather than part of a
// quoted path.
func endsInUnquotedBackslash(s string) bool {
	if !strings.HasSuffix(s, "\\") {
		return false
	}
	inQuotes := false
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '"' {
			inQuotes = !inQuotes
		}
	}
	return !inQuotes
}

// scanOutsideQuotes finds the first occurrence of any byte in seps within s
// that is outside a double-quoted region. Because a doubled "" (an escaped
// literal quote) toggles the in-quotes state twice in immediate succession,
// naive toggling on '"' is sufficient here: there is never a real character
// positioned between the two quotes of an escape pair for the toggle to
// misclassify.
func scanOutsideQuotes(s string, seps string) (idx int, sep byte, found bool) {
	inQuotes := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			inQuotes = !inQuotes
			continue
		}
		if !inQuotes && strings.IndexByte(seps, c) >= 0 {
			return i, c, true
		}
	}
	return 0, 0, false
}

// parseSectionHeader parses a "[Name]" (or "[\"Name\"]") header, trimmed of
// surrounding whitespace, returning the decoded section name.
func parseSectionHeader(trimmed string) (string, error) {
	end := strings.IndexByte(trimmed, ']')
	if end < 0 {
		return "", fmt.Errorf("unterminated section header %q", trimmed)
	}
	inner := trimmed[1:end]
	if len(inner) >= 2 && inner[0] == '"' && inner[len(inner)-1] == '"' {
		inner = inner[1 : len(inner)-1]
	}
	return inner, nil
}

// parseEntry parses one logical, non-section, non-comment, non-blank line
// into an Entry: "Key = field, field, ..." or a bare "field, field, ...".
func parseEntry(code, comment string) Entry {
	e := Entry{Comment: comment}
	if idx, _, found := scanOutsideQuotes(code, "="); found {
		e.HasKey = true
		e.Key = strings.TrimSpace(code[:idx])
		code = code[idx+1:]
	}
	e.Fields = splitFields(code)
	return e
}

// splitFields splits a comma-separated field list (outside quoted regions),
// trimming and unquoting each field per the INF quoting rules. A bare,
// all-whitespace field list yields no fields (a keyless line with no
// content), matching a directive that supplies zero fields.
func splitFields(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var fields []string
	for {
		idx, _, found := scanOutsideQuotes(s, ",")
		var part string
		if found {
			part, s = s[:idx], s[idx+1:]
		} else {
			part, s = s, ""
		}
		fields = append(fields, unquoteField(strings.TrimSpace(part)))
		if !found {
			break
		}
	}
	return fields
}

// unquoteField strips one enclosing layer of double quotes from a trimmed
// field, if present, collapsing any doubled "" escape sequences within it to
// a single literal '"'.
func unquoteField(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}
