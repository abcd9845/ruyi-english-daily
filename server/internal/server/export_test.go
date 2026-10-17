package server

import (
	"context"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/attempts"
)

// Test-only window into the queue state for the external server_test package.
type TItem struct {
	QRef, Enq, Reason, ItemID string
	Tries                     int
	NextTry                   time.Time
}
type TLink struct {
	SessionTotal int
	AnsweredAt   time.Time
	Items        []TItem
}

func (s *Server) TLink(cid string) *TLink {
	l := s.q.links.get(cid)
	if l == nil {
		return nil
	}
	t := &TLink{SessionTotal: l.SessionTotal, AnsweredAt: l.AnsweredAt}
	for _, it := range l.Items {
		t.Items = append(t.Items, TItem{it.QRef, it.Enq, it.Reason, it.ItemID, it.Tries, it.NextTry})
	}
	return t
}

// TLegacyQueued rewrites a stored link as an older release would have left it:
// no session_total, and the first answer already queued at the brain.
func (s *Server) TLegacyQueued(cid string) {
	l := s.q.links.get(cid).clone()
	l.SessionTotal = 0
	l.Items[0].Enq, l.Items[0].ItemID, l.Items[0].Reason = enqOK, "stub-item", ""
	_ = s.q.links.put(l, time.Now())
}

func TSummaryLine(r *attempts.Record) string { return summaryLine(r) }

func (s *Server) TNotQueuedLabel(reason string) string {
	return s.itemView(linkItem{QRef: "q", Enq: enqNotQueued, Reason: reason}, nil, time.Now(), nil).Label
}

func TEnqBackoff(n int) time.Duration { return enqBackoff(n) }

// TPollStep runs one poller step like the loop does and returns the wait before the next one.
func (s *Server) TPollStep(ctx context.Context) time.Duration { return s.poll.step(ctx) }

// TSetSessionTotal damages the stored session_total (state corruption) to provoke the brain's mismatch answer.
func (s *Server) TSetSessionTotal(cid string, n int) {
	l := s.q.links.get(cid).clone()
	l.SessionTotal = n
	_ = s.q.links.put(l, time.Now())
}

// TSetAnsweredAt damages the stored answered_at (state corruption) to provoke the brain's idempotency_conflict.
func (s *Server) TSetAnsweredAt(cid string, at time.Time) {
	l := s.q.links.get(cid).clone()
	l.AnsweredAt = at
	_ = s.q.links.put(l, time.Now())
}
