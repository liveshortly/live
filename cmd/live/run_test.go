package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestParseRunFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    runOpts
		rest    []string
		wantErr bool
	}{
		{
			name: "bare agent",
			args: []string{"claude"},
			want: runOpts{webPerms: true},
			rest: []string{"claude"},
		},
		{
			name: "title then agent with args",
			args: []string{"--title", "my run", "claude", "--model", "opus"},
			want: runOpts{title: "my run", webPerms: true},
			rest: []string{"claude", "--model", "opus"},
		},
		{
			name: "no-permissions",
			args: []string{"--no-permissions", "claude"},
			want: runOpts{webPerms: false},
			rest: []string{"claude"},
		},
		{
			name: "session fork",
			args: []string{"--session", "sess_123", "claude"},
			want: runOpts{webPerms: true, sessionID: "sess_123"},
			rest: []string{"claude"},
		},
		{
			name: "handoff fork with pre-digest",
			args: []string{"--handoff", "ho_abc", "--pre-digest", "gemini"},
			want: runOpts{webPerms: true, handoffCode: "ho_abc", preDigest: true},
			rest: []string{"gemini"},
		},
		{
			name: "double-dash sends everything (incl --session) to the agent",
			args: []string{"--title", "t", "--", "--session", "notaflag"},
			want: runOpts{title: "t", webPerms: true},
			rest: []string{"--session", "notaflag"},
		},
		{
			name: "fork flag AFTER the agent is honored (the acceptance form)",
			args: []string{"claude", "--session", "sess_9"},
			want: runOpts{webPerms: true, sessionID: "sess_9"},
			rest: []string{"claude"},
		},
		{
			name: "fork flag after agent, other agent args pass through",
			args: []string{"claude", "--model", "opus", "--handoff", "ho_z", "--verbose"},
			want: runOpts{webPerms: true, handoffCode: "ho_z"},
			rest: []string{"claude", "--model", "opus", "--verbose"},
		},
		{
			name: "pre-digest after the agent",
			args: []string{"claude", "--session", "s1", "--pre-digest"},
			want: runOpts{webPerms: true, sessionID: "s1", preDigest: true},
			rest: []string{"claude"},
		},
		{
			name:    "mutual exclusivity across placements (leading + post-agent)",
			args:    []string{"--handoff", "ho_a", "claude", "--session", "s_b"},
			wantErr: true,
		},
		{
			name:    "handoff and session are mutually exclusive",
			args:    []string{"--handoff", "ho_a", "--session", "s_b", "claude"},
			wantErr: true,
		},
		{
			name:    "title needs a value",
			args:    []string{"--title"},
			wantErr: true,
		},
		{
			name:    "session needs a value",
			args:    []string{"--session"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rest, err := parseRunFlags(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got opts=%+v rest=%v", got, rest)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("opts = %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(rest, tc.rest) {
				t.Fatalf("rest = %v, want %v", rest, tc.rest)
			}
		})
	}
}

// TestChannelArgsDoNotSwallowPrompt guards the arg ordering in channelArgs:
// --dangerously-load-development-channels is variadic and eats every following
// non-flag token as a channel name. Callers append the user's args (and, on a
// fork/handoff, the seeded opening prompt) AFTER these, so if the channel flag
// were last, a trailing prompt positional would be consumed as a bogus channel
// and claude would start with no prompt and exit instantly. The flag must be
// followed by a flag token (here the boolean --strict-mcp-config), which
// terminates the variadic and lets the prompt through.
func TestChannelArgsDoNotSwallowPrompt(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	t.Setenv("LIVE_CHANNEL", "") // ensure channels are enabled
	args, cleanup := channelArgs(self, nil /* interactive, not -p */)
	if cleanup != nil {
		defer cleanup()
	}
	if args == nil {
		t.Skip("channelArgs returned nil (mcp config unavailable in this env)")
	}

	i := indexOf(args, "--dangerously-load-development-channels")
	if i < 0 {
		t.Fatalf("channel flag not present in %v", args)
	}
	// Its single value must be followed by a flag token, so the variadic stops
	// before any appended positional prompt.
	if i+2 >= len(args) {
		t.Fatalf("channel flag+value are at the tail of %v — a trailing prompt would be swallowed", args)
	}
	if next := args[i+2]; !strings.HasPrefix(next, "-") {
		t.Fatalf("token after the channel value is %q (not a flag) — variadic would keep eating in %v", next, args)
	}
	// Belt and suspenders: the whole slice must end on a flag, so appending a
	// bare prompt positional is always safe.
	if last := args[len(args)-1]; !strings.HasPrefix(last, "-") {
		t.Fatalf("channelArgs ends with non-flag %q; appended prompt is unsafe: %v", last, args)
	}
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}
