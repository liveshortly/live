// Package perm brokers one tool-permission decision that can be answered from
// more than one place at once — a tmux popup on the CLI or the web
// (`/decision`) — whichever responds first wins.
package perm

import (
	"context"
	"sync"
	"time"
)

// Pending is one in-flight permission request. Exactly one Resolve wins.
type Pending struct {
	Tool   string
	Detail string

	res  chan result
	done chan struct{}
	once sync.Once
	dec  string
	src  string
}

type result struct{ dec, src string }

// Resolve records the first decision ("allow"/"deny"/"always"); later calls
// are no-ops. Returns true if this call was the winner.
func (p *Pending) Resolve(decision, source string) bool {
	won := false
	p.once.Do(func() {
		p.dec, p.src = decision, source
		p.res <- result{decision, source}
		close(p.done)
		won = true
	})
	return won
}

func (p *Pending) Done() <-chan struct{}      { return p.done }
func (p *Pending) Decision() (string, string) { return p.dec, p.src }

// Broker coordinates one pending permission at a time between the web poller
// (owned here) and the CLI popup (owned by the tmux driver).
type Broker struct {
	webPoll func(context.Context) (string, error) // "allow"/"deny"/"always"/""
	grace   time.Duration
	notify  chan *Pending

	mu  sync.Mutex
	cur *Pending
}

func New(webPoll func(context.Context) (string, error), grace time.Duration) *Broker {
	return &Broker{webPoll: webPoll, grace: grace, notify: make(chan *Pending, 1)}
}

// Notify delivers each new pending request to the tmux driver (to raise the
// popup). Buffered depth 1 — permissions are strictly sequential.
func (b *Broker) Notify() <-chan *Pending { return b.notify }

// Current is the pending request the CLI popup should answer, or nil.
func (b *Broker) Current() *Pending {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cur
}

// Request blocks until the permission is resolved by the CLI popup, a web
// verdict, or the grace timeout (→ deny). Returns (decision, source).
func (b *Broker) Request(ctx context.Context, tool, detail string) (string, string) {
	p := &Pending{Tool: tool, Detail: detail, res: make(chan result, 1), done: make(chan struct{})}
	b.mu.Lock()
	b.cur = p
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.cur == p {
			b.cur = nil
		}
		b.mu.Unlock()
	}()

	select {
	case b.notify <- p: // raise the tmux popup
	default:
	}

	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-t.C:
				if b.webPoll == nil {
					continue
				}
				if v, err := b.webPoll(pollCtx); err == nil && v != "" {
					p.Resolve(v, "web")
					return
				}
			}
		}
	}()

	timer := time.NewTimer(b.grace)
	defer timer.Stop()
	select {
	case r := <-p.res:
		return r.dec, r.src
	case <-timer.C:
		p.Resolve("deny", "timeout")
	case <-ctx.Done():
		p.Resolve("deny", "cancelled")
	}
	r := <-p.res
	return r.dec, r.src
}
