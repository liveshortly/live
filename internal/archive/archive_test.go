package archive

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAppendRoundTripAndDedupe(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "sub", "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	d.Append("s1", 0, 0, "agent", "prompt", map[string]any{"content": "hi"}, now)
	d.Append("s1", 0, 0, "agent", "response", map[string]any{"content": "yo"}, now)
	d.Append("s1", 7, 2, "agent", "response", map[string]any{"content": "dup"}, now) // same (s1,2): ignored
	rows, err := d.db.Query(`SELECT ord, event_type, payload FROM events WHERE session_id='s1' ORDER BY ord`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var ord int
		var et, p string
		rows.Scan(&ord, &et, &p)
		got = append(got, et+":"+p)
	}
	want := []string{`prompt:{"content":"hi"}`, `response:{"content":"yo"}`}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v want %v", got, want)
	}
}
