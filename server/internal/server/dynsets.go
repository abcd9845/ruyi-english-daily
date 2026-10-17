package server

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/sets"
)

const (
	maxDynSets = 200
	idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// pickMeta is what the brain said about one picked question.
type pickMeta struct {
	Origin   string // review | wrong
	Deferred string // deferred:<code> reason code; "" = gradable
}

type dynEntry struct {
	set  sets.Set
	exp  time.Time
	meta map[string]pickMeta
}

// dynSets holds generated practice sets in memory until their TTL ends.
// (A restart forgets them; the page then says the set no longer exists.)
type dynSets struct {
	mu  sync.Mutex
	m   map[string]*dynEntry
	ttl time.Duration
	now func() time.Time
}

func newDynSets(ttl time.Duration, now func() time.Time) *dynSets {
	if now == nil {
		now = time.Now
	}
	return &dynSets{m: map[string]*dynEntry{}, ttl: ttl, now: now}
}

func (d *dynSets) purgeLocked() {
	t := d.now()
	for k, e := range d.m {
		if !t.Before(e.exp) {
			delete(d.m, k)
		}
	}
}

// create stores a set under a fresh "auto-xxxxxxxx" id. ok=false when full.
func (d *dynSets) create(title, source string, qrefs []string, meta map[string]pickMeta) (sets.Set, time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.purgeLocked()
	if len(d.m) >= maxDynSets {
		return sets.Set{}, time.Time{}, false
	}
	for {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		for i := range b {
			b[i] = idAlphabet[int(b[i])%len(idAlphabet)]
		}
		id := sets.AutoPrefix + string(b)
		if _, taken := d.m[id]; taken {
			continue
		}
		e := &dynEntry{set: sets.Set{ID: id, Title: title, Source: source, QRefs: append([]string(nil), qrefs...)}, exp: d.now().Add(d.ttl), meta: meta}
		d.m[id] = e
		return e.set, e.exp, true
	}
}

func (d *dynSets) get(id string) (*sets.Set, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.m[id]
	if !ok || !d.now().Before(e.exp) {
		delete(d.m, id)
		return nil, false
	}
	s := e.set
	return &s, true
}

// metaOf returns the per-question brain notes of a live dynamic set.
func (d *dynSets) metaOf(id string) map[string]pickMeta {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.m[id]; ok && d.now().Before(e.exp) {
		return e.meta
	}
	return nil
}

func (d *dynSets) hasSource(source string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.purgeLocked()
	for _, e := range d.m {
		if e.set.Source == source {
			return true
		}
	}
	return false
}

func (d *dynSets) allowsQRef(source, qref string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.purgeLocked()
	for _, e := range d.m {
		if e.set.Source != source {
			continue
		}
		for _, q := range e.set.QRefs {
			if q == qref {
				return true
			}
		}
	}
	return false
}
