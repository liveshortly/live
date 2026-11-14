package main

import "testing"

func TestPiModelsParsesTable(t *testing.T) {
	out := `provider         model                       context  max-out  thinking  images
anthropic        claude-opus-5-5             1M       128K     yes       yes
qwen-uncensored  qwen3.6-35b-uncensored      32.8K    8.2K     yes       no

openai           gpt-6.1-sol                 400K     128K     yes       yes
`
	got := piModels(out)
	if len(got) != 3 {
		t.Fatalf("want 3 models, got %d: %+v", len(got), got)
	}
	if got[0].ID != "anthropic/claude-opus-5-5" || got[0].Label != "claude-opus-5-5" {
		t.Errorf("row 0 = %+v", got[0])
	}
	if got[2].ID != "openai/gpt-6.1-sol" {
		t.Errorf("row 2 = %+v", got[2])
	}
}

func TestPermOptions(t *testing.T) {
	cases := []struct {
		opts         []string
		wantOK       bool
		allow, block string
	}{
		{[]string{"Allow", "Block"}, true, "Allow", "Block"},
		{[]string{"Yes", "No"}, true, "Yes", "No"},
		{[]string{"Approve", "Reject"}, true, "Approve", "Reject"},
		{[]string{"Red", "Green", "Blue"}, false, "", ""}, // genuine question
		{[]string{"Option A", "Option B"}, false, "", ""}, // not permission-shaped
	}
	for _, c := range cases {
		allow, block, ok := permOptions(c.opts)
		if ok != c.wantOK || allow != c.allow || block != c.block {
			t.Errorf("permOptions(%v) = (%q,%q,%v), want (%q,%q,%v)",
				c.opts, allow, block, ok, c.allow, c.block, c.wantOK)
		}
	}
}
