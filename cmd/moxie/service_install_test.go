package main

import (
	"strings"
	"testing"
)

func TestServiceCommandArgs(t *testing.T) {
	got := serviceCommandArgs(serviceInstallOptions{
		cwd:       "/tmp/work",
		transport: "slack",
	})
	want := []string{"serve", "--cwd", "/tmp/work", "--transport", "slack"}
	if len(got) != len(want) {
		t.Fatalf("serviceCommandArgs() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("serviceCommandArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSystemdUnitContentsIncludesExecStartAndReload(t *testing.T) {
	unit, err := systemdUnitContents("/usr/local/bin/moxie", serviceInstallOptions{
		cwd:       "/srv/moxie",
		transport: "telegram",
	})
	if err != nil {
		t.Fatalf("systemdUnitContents() err = %v", err)
	}
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/moxie serve --cwd /srv/moxie --transport telegram") {
		t.Fatalf("unit missing ExecStart: %q", unit)
	}
	if !strings.Contains(unit, "ExecReload=/bin/kill -HUP $MAINPID") {
		t.Fatalf("unit missing ExecReload: %q", unit)
	}
}

func TestLaunchdPlistContentsIncludesLabelArgsAndLogs(t *testing.T) {
	plist, err := launchdPlistContents("/opt/homebrew/bin/moxie", serviceInstallOptions{
		cwd:       "/Users/you/projects/default",
		transport: "slack",
	})
	if err != nil {
		t.Fatalf("launchdPlistContents() err = %v", err)
	}
	for _, needle := range []string{
		"<string>" + defaultLaunchdLabel + "</string>",
		"<string>/opt/homebrew/bin/moxie</string>",
		"<string>serve</string>",
		"<string>--cwd</string>",
		"<string>/Users/you/projects/default</string>",
		"<string>--transport</string>",
		"<string>slack</string>",
		"<key>KeepAlive</key>",
		"<key>StandardOutPath</key>",
		"<key>StandardErrorPath</key>",
	} {
		if !strings.Contains(plist, needle) {
			t.Fatalf("plist missing %q: %q", needle, plist)
		}
	}
}

func TestServiceSuccessMessage(t *testing.T) {
	cases := map[string]string{
		"start":   "Service started",
		"stop":    "Service stopped",
		"restart": "Service restarted",
		"reload":  "Service reloaded",
		"status":  "",
	}
	for action, want := range cases {
		if got := serviceSuccessMessage(action); got != want {
			t.Fatalf("serviceSuccessMessage(%q) = %q, want %q", action, got, want)
		}
	}
}
