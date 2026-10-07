package main

import "testing"

func TestSelfHostedUpdatesUseIndependentReleaseOrigin(t *testing.T) {
	t.Setenv("LIVE_INSTALL_BASE", "")
	t.Setenv("LIVE_GH_BASE", "")
	t.Setenv("LIVE_LATEST_URL", "")
	t.Setenv("LIVESHORTLY_WEB_URL", "https://my-server.example.test")
	if got := installBase(); got != "https://github.com/liveshortly/live-dist/releases/download" {
		t.Fatal(got)
	}
	if got := latestReleaseURL(); got != "https://github.com/liveshortly/live-dist/releases/latest/download/latest.txt" {
		t.Fatal(got)
	}
	t.Setenv("LIVE_INSTALL_BASE", "https://downloads.example.test/live/")
	if got := installBase(); got != "https://downloads.example.test/live" {
		t.Fatal(got)
	}
	if got := latestReleaseURL(); got != "https://downloads.example.test/live/latest.txt" {
		t.Fatal(got)
	}
}
