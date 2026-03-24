package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Bug class: schedule plists lose PATH when moxie restarts with minimal env
// Root cause: launchdScheduleEnvironment blindly read os.Getenv("PATH")
// ---------------------------------------------------------------------------

// TestScheduleEnvFallsBackToServicePlistWhenPATHIsMinimal verifies that
// when the current process has a minimal PATH (as happens inside a
// launchd-spawned process), the schedule plist inherits PATH from the
// main service plist instead.
func TestScheduleEnvFallsBackToServicePlistWhenPATHIsMinimal(t *testing.T) {
	home := t.TempDir()

	// Create a service plist with a rich PATH.
	richPATH := "/Users/george/go/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	servicePlist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>io.github.1broseidon.moxie</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>` + richPATH + `</string>
    <key>HOME</key>
    <string>` + home + `</string>
  </dict>
</dict>
</plist>`
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plistDir, "io.github.1broseidon.moxie.plist"), []byte(servicePlist), 0o644); err != nil {
		t.Fatal(err)
	}

	// Override HOME so readServicePlistPATH finds our temp service plist.
	t.Setenv("HOME", home)

	// Set a minimal PATH (simulates launchd restart with stripped env).
	t.Setenv("PATH", "/usr/bin:/bin")

	env := launchdScheduleEnvironment()

	if env["PATH"] != richPATH {
		t.Fatalf("PATH = %q, want %q (should fall back to service plist)", env["PATH"], richPATH)
	}
}

// TestScheduleEnvUsesProcessPATHWhenRich verifies that when the
// current process has a full PATH, it is used directly.
func TestScheduleEnvUsesProcessPATHWhenRich(t *testing.T) {
	fullPATH := "/Users/george/go/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	t.Setenv("PATH", fullPATH)
	t.Setenv("HOME", "/Users/george")

	env := launchdScheduleEnvironment()

	if env["PATH"] != fullPATH {
		t.Fatalf("PATH = %q, want %q", env["PATH"], fullPATH)
	}
}

// TestScheduleEnvFallsBackToDefaultWhenNoPlistExists verifies the
// double-fallback: minimal PATH + no service plist = built-in default.
func TestScheduleEnvFallsBackToDefaultWhenNoPlistExists(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // No service plist here.
	t.Setenv("PATH", "/usr/bin:/bin")

	env := launchdScheduleEnvironment()

	if env["PATH"] != launchdScheduleDefaultPATH {
		t.Fatalf("PATH = %q, want default %q", env["PATH"], launchdScheduleDefaultPATH)
	}
}

// TestScheduleEnvFallsBackToDefaultWhenPATHEmpty handles the edge case
// where PATH is completely unset.
func TestScheduleEnvFallsBackToDefaultWhenPATHEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")

	env := launchdScheduleEnvironment()

	if env["PATH"] != launchdScheduleDefaultPATH {
		t.Fatalf("PATH = %q, want default %q", env["PATH"], launchdScheduleDefaultPATH)
	}
}

// TestIsMinimalPATHBoundary tests the 4-entry threshold precisely.
func TestIsMinimalPATHBoundary(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"empty", "", true},
		{"whitespace", "   ", true},
		{"one", "/usr/bin", true},
		{"two", "/usr/bin:/bin", true},
		{"three", "/usr/local/bin:/usr/bin:/bin", true},
		{"four (boundary)", "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin", true},
		{"five (over)", "/home/user/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin", false},
		{"ten", "/a:/b:/c:/d:/e:/f:/g:/h:/i:/j", false},
		{"typical user", "/Users/george/.npm-global/bin:/Users/george/go/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMinimalPATH(tc.path); got != tc.want {
				t.Errorf("isMinimalPATH(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestExtractPlistStringValueEdgeCases covers various malformed or
// unusual plist content.
func TestExtractPlistStringValueEdgeCases(t *testing.T) {
	cases := []struct {
		name  string
		plist string
		key   string
		want  string
	}{
		{"empty plist", "", "PATH", ""},
		{"missing key", "<key>HOME</key>\n<string>/Users/x</string>", "PATH", ""},
		{"missing string tag", "<key>PATH</key>\n<integer>42</integer>", "PATH", ""},
		{"empty value", "<key>PATH</key>\n<string></string>", "PATH", ""},
		{"whitespace value", "<key>PATH</key>\n<string>  /usr/bin  </string>", "PATH", "/usr/bin"},
		{"xml entities", "<key>PATH</key>\n<string>/a&amp;b</string>", "PATH", "/a&amp;b"},
		{"multiline gap", "<key>PATH</key>\n\n\n    <string>/usr/bin</string>", "PATH", "/usr/bin"},
		{
			"real plist",
			`<?xml version="1.0"?>
<plist version="1.0">
<dict>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    <key>HOME</key>
    <string>/Users/tester</string>
  </dict>
</dict>
</plist>`,
			"HOME",
			"/Users/tester",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractPlistStringValue(tc.plist, tc.key); got != tc.want {
				t.Errorf("extractPlistStringValue(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Bug class: schedule plist generated with wrong env inherits bad PATH
// Integration test: create a schedule with a launchd backend using a
// minimal-PATH env function and verify the plist content is correct.
// ---------------------------------------------------------------------------

// TestLaunchdBackendInstallUsesRobustPATH creates a full schedule via
// the launchd backend and verifies the generated plist contains the
// expected PATH from the env function (which simulates the fallback).
func TestLaunchdBackendInstallUsesRobustPATH(t *testing.T) {
	home := t.TempDir()
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	logDir := filepath.Join(home, "Library", "Logs")
	workDir := filepath.Join(home, "Library", "Application Support", "Moxie", "workspace")
	for _, d := range []string{plistDir, logDir, workDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	expectedPATH := "/Users/tester/go/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	runner := &recordingCommandRunner{
		failures:       map[string]error{},
		prefixFailures: map[string]error{},
	}
	backend := &launchdBackend{
		homeDir:    func() (string, error) { return home, nil },
		binaryPath: func() (string, error) { return "/opt/homebrew/bin/moxie", nil },
		now:        func() time.Time { return time.Date(2026, 3, 24, 12, 0, 0, 0, time.Local) },
		uid:        func() int { return 501 },
		env: func() map[string]string {
			return map[string]string{
				"PATH": expectedPATH,
				"HOME": home,
			}
		},
		runCommand: runner.run,
	}

	sc := Schedule{
		ID: "sch-env-test",
		Spec: ScheduleSpec{
			Trigger:  TriggerInterval,
			Interval: "5m",
		},
		Action: ActionExec,
	}

	if err := backend.Install(sc); err != nil {
		t.Fatalf("Install(): %v", err)
	}

	plistPath := launchdSchedulePlistPath(home, sc.ID)
	data, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	plist := string(data)

	if !strings.Contains(plist, "<string>"+expectedPATH+"</string>") {
		t.Fatalf("plist missing expected PATH:\n%s", plist)
	}

	// Verify it also has HOME.
	if !strings.Contains(plist, "<string>"+home+"</string>") {
		t.Fatalf("plist missing HOME:\n%s", plist)
	}
}

// TestLaunchdBackendUpdatePreservesPATH ensures that updating an
// existing schedule preserves the robust PATH in the new plist.
func TestLaunchdBackendUpdatePreservesPATH(t *testing.T) {
	home := t.TempDir()
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	logDir := filepath.Join(home, "Library", "Logs")
	workDir := filepath.Join(home, "Library", "Application Support", "Moxie", "workspace")
	for _, d := range []string{plistDir, logDir, workDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	expectedPATH := "/custom/path:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	runner := &recordingCommandRunner{
		failures:       map[string]error{},
		prefixFailures: map[string]error{},
	}
	backend := &launchdBackend{
		homeDir:    func() (string, error) { return home, nil },
		binaryPath: func() (string, error) { return "/opt/homebrew/bin/moxie", nil },
		now:        func() time.Time { return time.Date(2026, 3, 24, 12, 0, 0, 0, time.Local) },
		uid:        func() int { return 501 },
		env: func() map[string]string {
			return map[string]string{
				"PATH": expectedPATH,
				"HOME": home,
			}
		},
		runCommand: runner.run,
	}

	sc := Schedule{
		ID: "sch-env-update",
		Spec: ScheduleSpec{
			Trigger:  TriggerInterval,
			Interval: "10m",
		},
		Action: ActionExec,
	}

	if err := backend.Install(sc); err != nil {
		t.Fatalf("Install(): %v", err)
	}

	// Update with a different interval.
	sc.Spec.Interval = "15m"
	if err := backend.Update(sc); err != nil {
		t.Fatalf("Update(): %v", err)
	}

	data, err := os.ReadFile(launchdSchedulePlistPath(home, sc.ID))
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}

	if !strings.Contains(string(data), "<string>"+expectedPATH+"</string>") {
		t.Fatalf("updated plist lost PATH:\n%s", string(data))
	}

	// Verify the interval was actually updated (900 seconds = 15m).
	if !strings.Contains(string(data), "<integer>900</integer>") {
		t.Fatalf("updated plist has wrong interval:\n%s", string(data))
	}
}

// ---------------------------------------------------------------------------
// Bug class: schedule working directory with spaces breaks shell commands
// ---------------------------------------------------------------------------

// TestScheduleWorkingDirectoryWithSpacesIsXMLEscaped verifies that
// paths containing spaces (like "Application Support") are properly
// escaped in the generated plist.
func TestScheduleWorkingDirectoryWithSpacesIsXMLEscaped(t *testing.T) {
	spec := launchdTriggerSpec{startInterval: 300}
	plist, err := launchdSchedulePlistContents("/opt/homebrew/bin/moxie", Schedule{ID: "sch-spaces"}, spec, launchdScheduleOptions{
		Label:      launchdScheduleLabel("sch-spaces"),
		WorkingDir: "/Users/george/Library/Application Support/Moxie/workspace",
		LogPath:    "/Users/george/Library/Logs/moxie-schedule.log",
		Env: map[string]string{
			"PATH": "/usr/bin:/bin",
			"HOME": "/Users/george",
		},
	})
	if err != nil {
		t.Fatalf("launchdSchedulePlistContents(): %v", err)
	}

	if !strings.Contains(plist, "<string>/Users/george/Library/Application Support/Moxie/workspace</string>") {
		t.Fatalf("plist mangled working directory with spaces:\n%s", plist)
	}
}

// TestScheduleXMLEscapesSpecialCharacters verifies that XML-sensitive
// characters in paths are properly escaped.
func TestScheduleXMLEscapesSpecialCharacters(t *testing.T) {
	spec := launchdTriggerSpec{startInterval: 300}
	plist, err := launchdSchedulePlistContents("/usr/local/bin/moxie", Schedule{ID: "sch-xml"}, spec, launchdScheduleOptions{
		Label:      launchdScheduleLabel("sch-xml"),
		WorkingDir: "/tmp/test&workspace",
		LogPath:    "/tmp/log<test>.log",
		Env: map[string]string{
			"PATH": "/a&b:/c<d:/e>f",
			"HOME": "/Users/test\"er",
		},
	})
	if err != nil {
		t.Fatalf("launchdSchedulePlistContents(): %v", err)
	}

	// & should be escaped.
	if strings.Contains(plist, "/tmp/test&workspace") && !strings.Contains(plist, "/tmp/test&amp;workspace") {
		t.Fatal("& in working directory was not XML-escaped")
	}
	if strings.Contains(plist, "log<test>") {
		t.Fatal("< or > in log path was not XML-escaped")
	}
}
