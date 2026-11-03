package harness

import "testing"

// The checked-in README / HARNESS-MATRIX / JSON must match Rows and testdata/.
func TestMatrixInSync(t *testing.T) {
	drifted, err := Sync("../..", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(drifted) > 0 {
		t.Fatalf("%v out of date: run `go run ./scripts/harnessmatrix`", drifted)
	}
}

func TestLookups(t *testing.T) {
	if !ForAgent("/usr/local/bin/claude").ModelSwitch || ForAgent("codex").ModelSwitch {
		t.Fatal("model switch: claude only")
	}
	if ForAgent("antigravity").Display != "Antigravity" || ForAgent("nope").Framework != "terminal" {
		t.Fatal("agent lookup")
	}
	if !ForFramework("ollama").TypedComments || ForFramework("terminal").TypedComments {
		t.Fatal("typed comments: ollama only")
	}
	if ForDisplay("Claude").Framework != "claude-code" {
		t.Fatal("display lookup")
	}
}
