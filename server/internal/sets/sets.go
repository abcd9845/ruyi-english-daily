// Package sets parses the operator's practice-set file (RUYI_SETS_FILE).
//
// The file maps a set id to a brain source and an ordered qref whitelist:
//
//	{"sets":[{"id":"demo-1","title":"…","source":"<source key>","qrefs":["<qref>", …]}]}
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

// AutoPrefix starts the ids of generated (dynamic) sets; a sets file may not use it.
const AutoPrefix = "auto-"

var idRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// ValidID reports whether id is a syntactically valid set id.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Set is one practice set.
type Set struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Source string   `json:"source"`
	QRefs  []string `json:"qrefs"`
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

// HasSource reports whether any set uses source.
func (r *Registry) HasSource(source string) bool {
	if r == nil {
		return false
	}
	for _, s := range r.byID {
		if s.Source == source {
			return true
		}
	}
	return false
}

// AllowsQRef reports whether qref is whitelisted by any set of source.
func (r *Registry) AllowsQRef(source, qref string) bool {
	if r == nil {
		return false
	}
	for _, s := range r.byID {
		if s.Source != source {
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
