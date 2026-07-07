package main

import (
	"reflect"
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
