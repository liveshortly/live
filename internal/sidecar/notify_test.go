package sidecar

import (
	"os"
	"testing"

	"github.com/liveshortly/live/internal/api"
)

func TestIconWrites(t *testing.T) {
	p := iconPath()
	if p == "" {
		t.Skip("no home dir in test env")
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("icon not written: %v", err)
	}
	if fi.Size() != int64(len(iconPNG)) {
		t.Fatalf("icon size %d != embedded %d", fi.Size(), len(iconPNG))
	}
	if len(iconPNG) < 1000 {
		t.Fatalf("embedded icon suspiciously small: %d bytes", len(iconPNG))
	}
}

// TestDesktopNotifyNoPanic just exercises the code path; real delivery is
// visual and OS-dependent, so we only assert it doesn't panic.
func TestDesktopNotifyNoPanic(t *testing.T) {
	desktopNotify([]api.Comment{{Username: "tester", Message: `hi "there"`}})
	desktopNotify(nil)
}
