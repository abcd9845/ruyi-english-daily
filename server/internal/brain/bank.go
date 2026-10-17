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

// ErrUnsupported means the brain cannot do what was asked yet (its endpoint is
// not deployed), e.g. "wrong items only" selection before pick-practice is
// live. Callers must report it ("brain interface not ready"), not emulate it.
var ErrUnsupported = errors.New("not supported by the brain yet")

// Selection modes understood by pick-practice. The empty mode is the legacy
// local pick (whole source, library order or shuffled) that needs no brain data.
const (
	ModeRandom    = ""
	ModeDueFirst  = "due_first"
	ModeWrongOnly = "wrong_only"
)

// SelectOpts controls SelectForPractice.
type SelectOpts struct {
	// Mode is "" (legacy local pick), "due_first" or "wrong_only" (brain-side data).
	Mode string
	// SkipDeferred drops picks whose reasons carry "deferred:*" (answer key not
	// ready / unsupported type / figure missing) instead of marking them.
	SkipDeferred bool
	// Order is "random" (default) or "seq" (library order). Legacy mode only.
	Order string
	// Seed makes "random" reproducible; nil = unpredictable. Legacy mode only.
	Seed *int64
}

// Pick is one selected question. Origin/Reasons/Deferred come from the brain's
// pick-practice reply; the legacy local pick fills only QRef.
type Pick struct {
	QRef    string
	Number  string
	Origin  string // "review" | "wrong" | ""
	Reasons []string
	// Deferred is the code of the first "deferred:<code>" reason (e.g.
	// unsupported_qtype, pending_key, figure_missing); "" = the brain can grade it.
	Deferred string
}

// Bank is everything the practice site needs from the brain. Lookup, selection
// and grading follow the "practice-site automatic plan" contract; while an
// endpoint is not deployed yet each method degrades (list fallback / explicit
// ErrUnsupported / archive-only, see the individual methods).
type Bank interface {
	// ListBySource returns the whole active source in library order (by question number).
	ListBySource(ctx context.Context, source string) ([]Question, error)
	// LookupByQrefs returns the questions among qrefs that exist (in source),
	// and the qrefs that do not.
	LookupByQrefs(ctx context.Context, source string, qrefs []string) (found map[string]Question, missing []string, err error)
	// SelectForPractice picks up to n questions of source for a fresh practice set.
	SelectForPractice(ctx context.Context, source string, n int, opts SelectOpts) ([]Pick, error)
	// Grade submits an attempt for grading and recording. See grade.go.
	Grade(ctx context.Context, req GradeRequest) (*GradeResponse, error)
}

var _ Bank = (*Client)(nil)

// ListBySource implements Bank with the real list endpoint.
func (c *Client) ListBySource(ctx context.Context, source string) ([]Question, error) {
	return c.Source(ctx, source, false)
}

// listLookup is the P1–P3 fallback of LookupByQrefs: read the whole source
// (read-only) and pick the qrefs.
func (c *Client) listLookup(ctx context.Context, source string, qrefs []string) (map[string]Question, []string, error) {
	all, err := c.ListBySource(ctx, source)
	if err != nil {
		return nil, nil, err
	}
	have := make(map[string]Question, len(all))
	for _, q := range all {
		if k := strings.ToLower(q.QRef); have[k].QRef == "" {
			have[k] = q
		}
	}
	found := map[string]Question{}
	missing := []string{}
	for _, r := range qrefs {
		if q, ok := have[strings.ToLower(r)]; ok {
			found[r] = q
		} else {
			missing = append(missing, r)
		}
	}
	return found, missing, nil
}

// SelectForPractice implements Bank.
//
//   - Mode "" (legacy): whole source, library order or shuffled, picked locally
//     from the list endpoint. No brain data is needed or invented.
//   - Mode due_first / wrong_only: GET pick-practice (see contract.go). If the
//     endpoint is not deployed (404/501/405) the result is ErrUnsupported; the
//     site never fakes brain-side selection.
func (c *Client) SelectForPractice(ctx context.Context, source string, n int, o SelectOpts) ([]Pick, error) {
	switch o.Mode {
	case ModeDueFirst, ModeWrongOnly:
		return c.pick(ctx, source, n, o)
	case ModeRandom:
	default:
		return nil, errors.New("unknown selection mode")
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
	out := make([]Pick, len(refs))
	for i, r := range refs {
		out[i] = Pick{QRef: r}
	}
	return out, nil
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
