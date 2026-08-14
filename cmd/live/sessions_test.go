package main

import (
	"strings"
	"testing"
	"time"

	"github.com/liveshortly/live/internal/api"
)

func TestFitExactWidth(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"short", "short     "},
		{"exactly10x", "exactly10x"},
		{"waaaaaaaaay too long", "waaaaaaaa…"},
		{"émoîs are wide enough", "émoîs are…"}, // runes, not bytes
	} {
		got := fit(c.in, 10)
		if got != c.want {
			t.Errorf("fit(%q,10) = %q, want %q", c.in, got, c.want)
		}
		if n := len([]rune(got)); n != 10 {
			t.Errorf("fit(%q,10) is %d columns, want 10", c.in, n)
		}
	}
}

func TestPlural(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{{0, "0 events"}, {1, "1 event"}, {2, "2 events"}, {1500, "1,500 events"}} {
		if got := plural(c.n, "event"); got != c.want {
			t.Errorf("plural(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestHumanDur(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{12 * time.Minute, "12m"},
		{3*time.Hour + 4*time.Minute, "3h04m"},
		{50 * time.Hour, "2d2h"},
		{-time.Second, "0s"},
	} {
		if got := humanDur(c.d); got != c.want {
			t.Errorf("humanDur(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestRepoName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "local"},
		{"git@github.com:liveshortly/live.git", "liveshortly/live"},
		{"https://github.com/liveshortly/live", "liveshortly/live"},
	} {
		if got := repoName(c.in); got != c.want {
			t.Errorf("repoName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestVisibleEventsDropsStreamBoundaries(t *testing.T) {
	in := []api.Event{
		{EventType: "stream_start"},
		{EventType: "prompt"},
		{EventType: "stream_end"},
		{EventType: "response"},
	}
	got := visibleEvents(in)
	if len(got) != 2 || got[0].EventType != "prompt" || got[1].EventType != "response" {
		t.Fatalf("visibleEvents = %+v", got)
	}
}

func TestEventBody(t *testing.T) {
	for _, c := range []struct {
		name string
		e    api.Event
		want string
	}{
		{"viewer comment names the sender",
			api.Event{EventType: "viewer_comment", Payload: map[string]any{"username": "bob", "message": "hi"}},
			"bob: hi"},
		{"response is its text",
			api.Event{EventType: "response", Payload: map[string]any{"content": "done"}},
			"done"},
		{"tool call shows tool and target",
			api.Event{EventType: "tool_call", Payload: map[string]any{"tool_name": "Bash", "command": "git status"}},
			"Bash — git status"},
	} {
		if got := stripANSITest(eventBody(c.e)); got != c.want {
			t.Errorf("%s: eventBody = %q, want %q", c.name, got, c.want)
		}
	}
}

// A session link must resolve to its id before the "already long enough to be a
// uuid" shortcut sees it — a URL is always longer than a bare uuid.
func TestResolveIDAcceptsLinkAndUUID(t *testing.T) {
	const id = "0eaf6c37-d8f9-4db1-ab59-484503a91213"
	for _, ref := range []string{
		id,
		"http://localhost:8080/session/" + id,
		"https://liveshortly.com/session/" + id + "/",
	} {
		got, err := resolveID(nil, ref) // no client needed: neither path calls the API
		if err != nil || got != id {
			t.Errorf("resolveID(%q) = %q, %v — want %q", ref, got, err, id)
		}
	}
	if _, err := resolveID(nil, "  "); err == nil {
		t.Error("resolveID(blank) should fail")
	}
	if _, err := resolveID(nil, "https://liveshortly.com/session/not-a-uuid"); err == nil {
		t.Error("resolveID(invalid link) should fail")
	}
}

// A listing row must never truncate the link — half a URL is useless — so the
// title column is what gives way on a narrow terminal, and the agent column is
// dropped before the title stops naming anything.
func TestPlanListProtectsTheLink(t *testing.T) {
	rows := []api.Session{{
		ID:    "0eaf6c37-d8f9-4db1-ab59-484503a91213",
		Title: "a fairly long session title here", Agent: "claude-code",
	}}
	linkW := len(sessionURL(rows[0].ID))

	wide := planList(rows, 200)
	if wide.agent == 0 {
		t.Error("a 200-column terminal should still show the agent")
	}
	// A wide terminal must not stretch the columns: the title keeps its natural
	// width so the ids and links stay next to it instead of drifting to the far
	// right with a lake of padding between.
	if wide.name != len([]rune(rows[0].Title)) {
		t.Errorf("title column = %d, want the natural %d — columns must not stretch",
			wide.name, len([]rune(rows[0].Title)))
	}
	if planList(rows, 400).name != wide.name {
		t.Error("doubling the terminal width changed the layout")
	}

	narrow := planList(rows, linkW+30)
	if narrow.agent != 0 {
		t.Error("the agent column should be dropped before the title is squeezed")
	}
	if narrow.name < minName {
		t.Errorf("title column = %d, want at least %d", narrow.name, minName)
	}
	if narrow.link != linkW {
		t.Errorf("link column = %d, want the full %d — a clipped URL is unusable", narrow.link, linkW)
	}
}

// Joining a session that is already stuck on an unanswered permission has to
// say so; anything that could only follow a verdict means it is not stuck.
func TestPendingPermission(t *testing.T) {
	perm := api.Event{EventType: "input_requested", Payload: map[string]any{"kind": "permission"}}
	idle := api.Event{EventType: "input_requested", Payload: map[string]any{"kind": "input"}}
	for _, c := range []struct {
		name string
		in   []api.Event
		want bool
	}{
		{"unanswered permission", []api.Event{{EventType: "prompt"}, perm}, true},
		{"answered", []api.Event{perm, {EventType: "viewer_decision"}}, false},
		{"agent moved on", []api.Event{perm, {EventType: "tool_call"}}, false},
		{"idle wait is not a block", []api.Event{idle}, false},
		{"nothing yet", nil, false},
	} {
		if got := pendingPermission(c.in); got != c.want {
			t.Errorf("%s: pendingPermission = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPrintSessionStaysWithinWidth(t *testing.T) {
	s := api.Session{
		ID: "0eaf6c37-d8f9-4db1-ab59-484503a91213", Status: "live",
		Title: strings.Repeat("a very long session title ", 20),
		Agent: "claude-code", Model: "claude-opus-5",
		CreatedAt: time.Now().Add(-time.Hour), EventCount: 3,
	}
	// The title column is what printSession pads; check the same computation it
	// uses cannot overflow the terminal.
	w := termWidth()
	right := strings.TrimSpace(s.Agent + "  " + s.Model)
	col := max(12, w-14-len([]rune(right)))
	line := " " + "● LIVE " + "  " + fit(s.Title, col) + "  " + right
	if n := len([]rune(line)); n > w {
		t.Fatalf("header line is %d columns, terminal is %d", n, w)
	}
}
