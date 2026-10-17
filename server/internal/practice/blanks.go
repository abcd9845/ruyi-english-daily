// Package practice holds the text rules shared (in spirit) with the practice
// page: where the math is, and where the fill-in blanks are.
//
// The page has a JavaScript twin of Segments/CountBlanks; both follow the same
// rules so the server's `blanks` equals the number of input boxes the page draws.
package practice

import "strings"

// Segment is a run of the stem: either plain text or a math fragment
// (delimiters included).
type Segment struct {
	Text string
	Math bool
}

// Segments splits a stem into plain and math parts. Recognized math:
//
//	$$ … $$   $ … $   \( … \)   \[ … \]
//
// A backslash-escaped dollar (\$) is plain text. An opener without a matching
// closer is plain text. Math fragments must not be empty.
func Segments(s string) []Segment {
	var out []Segment
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			out = append(out, Segment{Text: plain.String()})
			plain.Reset()
		}
	}
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '$':
				plain.WriteString(`\$`)
				i += 2
				continue
			case '(':
				if j := strings.Index(s[i+2:], `\)`); j > 0 {
					flush()
					out = append(out, Segment{Text: s[i : i+2+j+2], Math: true})
					i += 2 + j + 2
					continue
				}
			case '[':
				if j := strings.Index(s[i+2:], `\]`); j > 0 {
					flush()
					out = append(out, Segment{Text: s[i : i+2+j+2], Math: true})
					i += 2 + j + 2
					continue
				}
			}
			plain.WriteByte(c)
			plain.WriteByte(s[i+1])
			i += 2
			continue
		}
		if c == '$' {
			if strings.HasPrefix(s[i:], "$$") {
				if j := strings.Index(s[i+2:], "$$"); j > 0 {
					flush()
					out = append(out, Segment{Text: s[i : i+2+j+2], Math: true})
					i += 2 + j + 2
					continue
				}
				plain.WriteString("$$")
				i += 2
				continue
			}
			if j := closeInline(s[i+1:]); j > 0 {
				flush()
				out = append(out, Segment{Text: s[i : i+1+j+1], Math: true})
				i += 1 + j + 1
				continue
			}
		}
		plain.WriteByte(c)
		i++
	}
	flush()
	return out
}

// closeInline finds the closing single $ of an inline fragment: the first $
// not preceded by a backslash.
func closeInline(rest string) int {
	for k := 0; k < len(rest); k++ {
		switch rest[k] {
		case '\\':
			k++
		case '$':
			return k
		}
	}
	return -1
}

// CountBlanks counts runs of four or more underscores (_ or the full-width ＿)
// in the plain (non-math) text of a stem.
func CountBlanks(stem string) int {
	n := 0
	for _, seg := range Segments(stem) {
		if seg.Math {
			continue
		}
		run := 0
		for _, r := range seg.Text {
			if r == '_' || r == '＿' {
				run++
				continue
			}
			if run >= 4 {
				n++
			}
			run = 0
		}
		if run >= 4 {
			n++
		}
	}
	return n
}
