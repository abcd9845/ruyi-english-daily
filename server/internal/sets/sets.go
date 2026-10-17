// Package sets parses the operator's practice-set file (RUYI_SETS_FILE).
//
// The file maps a set id to a brain source and an ordered qref whitelist:
//
//	{"sets":[{"id":"demo-1","title":"…","source":"<source key>","qrefs":["<qref>", …]}]}
//
// A set with "local": true is a self-contained test set: its questions are inline
// ("questions"), it is never sent to the brain, and it lives in a reserved
// namespace (ids smoke-*/test-*, qrefs smoke:*, source local.smoke) that real
// sets may not use.
//
// Parsing is strict (unknown fields, duplicate ids, empty lists are errors).
// Error messages never include values from the file: they name the position
// ("sets[2].id") and the problem only, so a real qref cannot leak into logs.
package sets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/abcd9845/ruyi-english-daily/server/internal/practice"
)

// Limits keep a typo from turning into a huge response.
const (
	MaxFileBytes   = 1 << 20
	MaxSets        = 200
	MaxQRefs       = 300
	maxTitleRunes  = 200
	maxQRefBytes   = 200
	maxSourceBytes = 128
)

// Local ("smoke") sets carry their own inline question and are never sent to
// the brain. They are fenced off from real papers on every side:
//
//   - the id must start with LocalIDPrefixSmoke or LocalIDPrefixTest, and real
//     sets may not use those prefixes;
//   - the source is the reserved LocalSource (it may be omitted, anything else
//     is refused), and config refuses it in RUYI_AUTO_SOURCES / RUYI_ALLOWED_SOURCES;
//   - every qref starts with LocalQRefPrefix, and real sets may not use it.
const (
	LocalIDPrefixSmoke = "smoke-"
	LocalIDPrefixTest  = "test-"
	LocalSource        = "local.smoke"
	LocalQRefPrefix    = "smoke:"

	maxLocalQuestions = 5
	maxStemRunes      = 2000
	maxOptions        = 8
	maxOptionRunes    = 500
	maxBlanksLocal    = 10
)

// IsLocalID reports whether id lies in the namespace reserved for local sets.
func IsLocalID(id string) bool {
	return strings.HasPrefix(id, LocalIDPrefixSmoke) || strings.HasPrefix(id, LocalIDPrefixTest)
}

// IsLocalQRef reports whether qref lies in the namespace reserved for local sets.
func IsLocalQRef(q string) bool { return strings.HasPrefix(strings.ToLower(q), LocalQRefPrefix) }

// AutoPrefix starts the ids of generated (dynamic) sets; a sets file may not use it.
const AutoPrefix = "auto-"

var idRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// ValidID reports whether id is a syntactically valid set id.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Option is one choice of a local question.
type Option struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

// Question is the inline question of a local set. Answer is for the server's
// own right/wrong check only: it is never copied into a response.
// single_choice: Answer = [option key]; fill_blank: one accepted text per blank.
type Question struct {
	QRef    string   `json:"qref"`
	QType   string   `json:"qtype"`
	Stem    string   `json:"stem"`
	Options []Option `json:"options,omitempty"`
	Answer  []string `json:"answer"`
}

// Set is one practice set.
type Set struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Source string   `json:"source"`
	QRefs  []string `json:"qrefs"`

	// Local marks a self-contained test set (see the constants above): its
	// questions are inline, it is never graded by or written to the brain.
	// For a local set QRefs is not written in the file; Parse derives it from Questions.
	Local     bool       `json:"local,omitempty"`
	Questions []Question `json:"questions,omitempty"`
}

// Question returns the inline question with qref (local sets only).
func (s *Set) Question(qref string) (*Question, bool) {
	if s == nil || !s.Local {
		return nil, false
	}
	for i := range s.Questions {
		if s.Questions[i].QRef == qref {
			return &s.Questions[i], true
		}
	}
	return nil, false
}

// Registry is an immutable collection of sets. The zero value (and nil) holds no sets.
type Registry struct {
	byID  map[string]*Set
	order []string
}

// Parse validates data. validSource checks a source key's syntax.
func Parse(data []byte, validSource func(string) bool) (*Registry, error) {
	if len(data) > MaxFileBytes {
		return nil, errors.New("file too large")
	}
	var f struct {
		Sets []Set `json:"sets"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		// json errors can quote offending values; say only that it is malformed.
		return nil, errors.New("not valid JSON of the expected shape (unknown or mistyped field?)")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON document")
	}
	if len(f.Sets) == 0 {
		return nil, errors.New(`"sets" is empty or missing`)
	}
	if len(f.Sets) > MaxSets {
		return nil, fmt.Errorf("too many sets (max %d)", MaxSets)
	}
	r := &Registry{byID: map[string]*Set{}}
	for i := range f.Sets {
		s := &f.Sets[i]
		at := func(field, why string) error { return fmt.Errorf("sets[%d].%s: %s", i, field, why) }
		if !idRe.MatchString(s.ID) {
			return nil, at("id", "must match [a-z0-9_-]{1,64}")
		}
		if s.ID == "new" || strings.HasPrefix(s.ID, AutoPrefix) {
			return nil, at("id", `"new" and ids starting with "auto-" are reserved`)
		}
		if _, dup := r.byID[s.ID]; dup {
			return nil, at("id", "duplicate id")
		}
		if t := utf8.RuneCountInString(s.Title); t == 0 || t > maxTitleRunes || !printable(s.Title) {
			return nil, at("title", "required, at most 200 characters, no control characters")
		}
		if s.Local {
			if err := parseLocal(s, at); err != nil {
				return nil, err
			}
			r.byID[s.ID] = s
			r.order = append(r.order, s.ID)
			continue
		}
		if IsLocalID(s.ID) {
			return nil, at("id", `ids starting with "smoke-" or "test-" are reserved for local sets`)
		}
		if len(s.Questions) > 0 {
			return nil, at("questions", `only allowed in a set with "local": true`)
		}
		if s.Source == LocalSource {
			return nil, at("source", "reserved for local sets")
		}
		if len(s.Source) > maxSourceBytes || !validSource(s.Source) {
			return nil, at("source", "not a valid source key")
		}
		if len(s.QRefs) == 0 {
			return nil, at("qrefs", "must not be empty")
		}
		if len(s.QRefs) > MaxQRefs {
			return nil, at("qrefs", fmt.Sprintf("too many (max %d)", MaxQRefs))
		}
		seen := map[string]bool{}
		for j, q := range s.QRefs {
			if q == "" || len(q) > maxQRefBytes || !utf8.ValidString(q) || !printable(q) || q != trimmed(q) {
				return nil, at(fmt.Sprintf("qrefs[%d]", j), "empty, too long, has control characters or surrounding spaces")
			}
			if IsLocalQRef(q) {
				return nil, at(fmt.Sprintf("qrefs[%d]", j), `the "smoke:" prefix is reserved for local sets`)
			}
			if seen[q] {
				return nil, at(fmt.Sprintf("qrefs[%d]", j), "duplicate qref")
			}
			seen[q] = true
		}
		r.byID[s.ID] = s
		r.order = append(r.order, s.ID)
	}
	return r, nil
}

// parseLocal validates a local set and derives its QRefs. It runs after the
// id/title/duplicate checks. Messages name positions only, never values.
func parseLocal(s *Set, at func(field, why string) error) error {
	if !strings.HasPrefix(s.ID, LocalIDPrefixSmoke) && !strings.HasPrefix(s.ID, LocalIDPrefixTest) {
		return at("id", `a local set's id must start with "smoke-" or "test-"`)
	}
	if len(s.QRefs) > 0 {
		return at("qrefs", `a local set must not list qrefs (its questions are inline)`)
	}
	if s.Source == "" {
		s.Source = LocalSource
	} else if s.Source != LocalSource {
		return at("source", "a local set's source is fixed (omit it)")
	}
	if len(s.Questions) == 0 {
		return at("questions", "a local set needs at least one question")
	}
	if len(s.Questions) > maxLocalQuestions {
		return at("questions", fmt.Sprintf("too many (max %d)", maxLocalQuestions))
	}
	seen := map[string]bool{}
	for j := range s.Questions {
		q := &s.Questions[j]
		qat := func(f, why string) error { return at(fmt.Sprintf("questions[%d].%s", j, f), why) }
		if q.QRef == "" || len(q.QRef) > maxQRefBytes || !utf8.ValidString(q.QRef) || !printable(q.QRef) || q.QRef != trimmed(q.QRef) {
			return qat("qref", "empty, too long, has control characters or surrounding spaces")
		}
		if !strings.HasPrefix(q.QRef, LocalQRefPrefix) {
			return qat("qref", `a local question's qref must start with "smoke:"`)
		}
		if seen[q.QRef] {
			return qat("qref", "duplicate qref")
		}
		seen[q.QRef] = true
		if n := utf8.RuneCountInString(q.Stem); strings.TrimSpace(q.Stem) == "" || n > maxStemRunes || !utf8.ValidString(q.Stem) || !printableText(q.Stem) {
			return qat("stem", "required, at most 2000 characters, no control characters")
		}
		switch q.QType {
		case "single_choice":
			if len(q.Options) < 2 || len(q.Options) > maxOptions {
				return qat("options", fmt.Sprintf("a single_choice question needs 2 to %d options", maxOptions))
			}
			keys := map[string]bool{}
			for k, o := range q.Options {
				if !optKeyRe.MatchString(o.Key) || keys[o.Key] {
					return qat(fmt.Sprintf("options[%d].key", k), "must be one unique capital letter")
				}
				keys[o.Key] = true
				if n := utf8.RuneCountInString(o.Text); strings.TrimSpace(o.Text) == "" || n > maxOptionRunes || !utf8.ValidString(o.Text) || !printableText(o.Text) {
					return qat(fmt.Sprintf("options[%d].text", k), "required, at most 500 characters, no control characters")
				}
			}
			if len(q.Answer) != 1 || !keys[q.Answer[0]] {
				return qat("answer", "must be exactly one of the option keys")
			}
		case "fill_blank":
			if len(q.Options) != 0 {
				return qat("options", "a fill_blank question has no options")
			}
			b := practice.CountBlanks(q.Stem)
			if b < 1 || b > maxBlanksLocal {
				return qat("stem", fmt.Sprintf("a fill_blank stem needs 1 to %d blanks (4+ underscores)", maxBlanksLocal))
			}
			if len(q.Answer) != b {
				return qat("answer", "needs exactly one entry per blank")
			}
			for k, a := range q.Answer {
				if strings.TrimSpace(a) == "" || utf8.RuneCountInString(a) > 200 || !utf8.ValidString(a) || !printable(a) {
					return qat(fmt.Sprintf("answer[%d]", k), "empty, too long or has control characters")
				}
			}
		default:
			return qat("qtype", "must be single_choice or fill_blank")
		}
		s.QRefs = append(s.QRefs, q.QRef)
	}
	return nil
}

var optKeyRe = regexp.MustCompile(`^[A-Z]$`)

// printableText is printable that also allows newlines and tabs.
func printableText(s string) bool {
	for _, c := range s {
		if c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}

func printable(s string) bool {
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

func trimmed(s string) string { return string(bytes.TrimSpace([]byte(s))) }

// Get returns the set with id.
func (r *Registry) Get(id string) (*Set, bool) {
	if r == nil {
		return nil, false
	}
	s, ok := r.byID[id]
	return s, ok
}

// Len is the number of sets.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.order)
}

// IDs lists set ids in file order.
func (r *Registry) IDs() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.order...)
}

// HasSource reports whether any real (non-local) set uses source.
func (r *Registry) HasSource(source string) bool {
	if r == nil {
		return false
	}
	for _, s := range r.byID {
		if !s.Local && s.Source == source {
			return true
		}
	}
	return false
}

// AllowsQRef reports whether qref is whitelisted by any real (non-local) set of source.
func (r *Registry) AllowsQRef(source, qref string) bool {
	if r == nil {
		return false
	}
	for _, s := range r.byID {
		if s.Local || s.Source != source {
			continue
		}
		for _, q := range s.QRefs {
			if q == qref {
				return true
			}
		}
	}
	return false
}
