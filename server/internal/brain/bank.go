package brain

import (
	"context"
	"errors"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ErrUnsupported means the brain (adapter) cannot do what was asked yet, e.g.
// "wrong items only" selection before the brain ships that endpoint. Callers
// must report it, not emulate it.
var ErrUnsupported = errors.New("not supported by the brain yet")

// SelectOpts controls SelectForPractice.
type SelectOpts struct {
	// WrongOnly asks for questions the learner got wrong before (brain-side data).
	WrongOnly bool
	// DueFirst asks for review-due questions first (brain-side data).
	DueFirst bool
	// Order is "random" (default) or "seq" (library order).
	Order string
	// Seed makes "random" reproducible; nil = unpredictable.
	Seed *int64
}

// Bank is everything the practice site needs from the brain. Today the real
// brain only offers the read-only question list, so ListBySource is real and
// the rest are adapters (see lookup/select below, grade.go) that are meant to be
// swapped for dedicated endpoints once the brain ships them: replace the
// function bodies, nothing else changes.
type Bank interface {
	// ListBySource returns the whole active source in library order (by question number).
	ListBySource(ctx context.Context, source string) ([]Question, error)
	// LookupByQrefs returns the questions among qrefs that exist in source, and
	// the qrefs that do not.
	LookupByQrefs(ctx context.Context, source string, qrefs []string) (found map[string]Question, missing []string, err error)
	// SelectForPractice picks up to n qrefs of source for a fresh practice set.
	SelectForPractice(ctx context.Context, source string, n int, opts SelectOpts) ([]string, error)
	// Grade submits an attempt for grading and recording. See grade.go.
	Grade(ctx context.Context, req GradeRequest) (*GradeResponse, error)
}

var _ Bank = (*Client)(nil)

// ListBySource implements Bank with the real list endpoint.
func (c *Client) ListBySource(ctx context.Context, source string) ([]Question, error) {
	return c.Source(ctx, source, false)
}

// LookupByQrefs implements Bank. ADAPTER: no dedicated lookup endpoint exists
// yet, so it reads the whole source (read-only) and picks the qrefs.
func (c *Client) LookupByQrefs(ctx context.Context, source string, qrefs []string) (map[string]Question, []string, error) {
	all, err := c.ListBySource(ctx, source)
	if err != nil {
		return nil, nil, err
	}
	have := make(map[string]Question, len(all))
	for _, q := range all {
		if _, dup := have[q.QRef]; !dup {
			have[q.QRef] = q
		}
	}
	found := map[string]Question{}
	missing := []string{}
	for _, r := range qrefs {
		if q, ok := have[r]; ok {
			found[r] = q
		} else {
			missing = append(missing, r)
		}
	}
	return found, missing, nil
}

// SelectForPractice implements Bank. ADAPTER: the brain has no selection
// endpoint yet, so plain "all of the source, library order or shuffled" is
// simulated here. Anything that needs brain-side data (wrong items, due
// reviews) returns ErrUnsupported; this code never invents such logic.
func (c *Client) SelectForPractice(ctx context.Context, source string, n int, o SelectOpts) ([]string, error) {
	if o.WrongOnly || o.DueFirst {
		return nil, ErrUnsupported
	}
	listed, err := c.ListBySource(ctx, source)
	if err != nil {
		return nil, err
	}
	all := append([]Question(nil), listed...) // never reorder the cached slice
	if o.Order == "seq" {
		sort.SliceStable(all, func(i, j int) bool { return seqLess(all[i], all[j]) })
	}
	refs := make([]string, 0, len(all))
	seen := map[string]bool{}
	for _, q := range all {
		if !seen[q.QRef] {
			seen[q.QRef] = true
			refs = append(refs, q.QRef)
		}
	}
	if o.Order != "seq" {
		var r *rand.Rand
		if o.Seed != nil {
			r = rand.New(rand.NewSource(*o.Seed))
		} else {
			r = rand.New(rand.NewSource(rand.Int63()))
		}
		// stable base order first so a seed is reproducible regardless of list order
		sort.Strings(refs)
		r.Shuffle(len(refs), func(i, j int) { refs[i], refs[j] = refs[j], refs[i] })
	}
	if n < len(refs) {
		refs = refs[:n]
	}
	return refs, nil
}

// seqNumRe accepts "7", "7(2)", "7(2)③": major, optional minor, suffix.
var seqNumRe = regexp.MustCompile(`^(\d+)(?:\((\d+)\))?(.*)$`)

func seqKey(q Question) (maj, min int, suf string, ok bool) {
	if q.Number == nil {
		return 0, 0, "", false
	}
	m := seqNumRe.FindStringSubmatch(strings.TrimSpace(*q.Number))
	if m == nil {
		return 0, 0, "", false
	}
	maj, e1 := strconv.Atoi(m[1])
	if m[2] != "" {
		min, _ = strconv.Atoi(m[2])
	}
	return maj, min, m[3], e1 == nil
}

// seqLess orders by question number; unnumbered questions go last and keep
// their library order (the caller sorts stably).
func seqLess(a, b Question) bool {
	am, an, as, aok := seqKey(a)
	bm, bn, bs, bok := seqKey(b)
	switch {
	case aok && bok:
		if am != bm {
			return am < bm
		}
		if an != bn {
			return an < bn
		}
		return as < bs
	case aok != bok:
		return aok
	}
	return false
}
