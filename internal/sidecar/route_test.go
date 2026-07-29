package sidecar

import (
	"testing"

	"github.com/liveshortly/live/internal/api"
)

// A comment the channel accepted must NOT also sit on the hook queue. When it
// did, every message was answered twice: the channel delivered it, then
// hookStop drained the same comment and blocked the turn to re-deliver it.
func TestRoutedCommentIsNotAlsoQueuedForHooks(t *testing.T) {
	s := testSidecar()
	ch, unsub := s.subscribe()

	c := api.Comment{Username: "rohit", Message: "hello"}
	undelivered := s.routeToChannel([]api.Comment{c})
	if len(undelivered) != 0 {
		t.Fatalf("routeToChannel returned %d undelivered, want 0", len(undelivered))
	}
	if got := <-ch; got.Message != "hello" {
		t.Fatalf("subscriber got %q, want hello", got.Message)
	}

	// The hook path must now find nothing to inject — otherwise Stop blocks the
	// turn and Claude answers the same comment a second time.
	if left := s.takePending(); len(left) != 0 {
		t.Fatalf("comment was also queued for hooks: %v", left)
	}
	unsub()
}

// With no channel subscriber the hook queue is the only delivery path, so
// every comment must land there.
func TestUnroutedCommentFallsBackToHooks(t *testing.T) {
	s := testSidecar()
	comments := []api.Comment{{Message: "one"}, {Message: "two"}}

	undelivered := s.routeToChannel(comments)
	if len(undelivered) != 2 {
		t.Fatalf("routeToChannel returned %d undelivered, want 2", len(undelivered))
	}
	s.queueForHooks(undelivered)

	got := s.takePending()
	if len(got) != 2 || got[0].Message != "one" || got[1].Message != "two" {
		t.Fatalf("hook queue = %v, want both comments in order", got)
	}
}

// Nothing may be lost when a subscriber goes away holding buffered comments:
// unsubscribing hands them back to the hook queue.
func TestUnsubscribeRequeuesBufferedComments(t *testing.T) {
	s := testSidecar()
	_, unsub := s.subscribe() // never read from

	if undelivered := s.routeToChannel([]api.Comment{{Message: "buffered"}}); len(undelivered) != 0 {
		t.Fatalf("expected the buffer to accept it, got %d undelivered", len(undelivered))
	}
	if left := s.takePending(); len(left) != 0 {
		t.Fatalf("should not be on the hook queue while the subscriber holds it: %v", left)
	}

	unsub()

	got := s.takePending()
	if len(got) != 1 || got[0].Message != "buffered" {
		t.Fatalf("hook queue after unsubscribe = %v, want the buffered comment", got)
	}
}

// A full subscriber buffer is not delivery — those comments must fall back to
// hooks rather than being silently dropped.
func TestFullSubscriberBufferFallsBackToHooks(t *testing.T) {
	s := testSidecar()
	_, unsub := s.subscribe()
	defer unsub()

	// subscribe()'s channel is buffered at 64; overfill it.
	many := make([]api.Comment, 70)
	for i := range many {
		many[i] = api.Comment{Message: "m"}
	}
	undelivered := s.routeToChannel(many)
	if len(undelivered) != 6 {
		t.Fatalf("undelivered = %d, want 6 (70 sent, 64 buffered)", len(undelivered))
	}
}
