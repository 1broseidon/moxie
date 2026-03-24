package main

import (
	"bufio"
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/1broseidon/moxie/internal/store"
)

type serviceInstallOptions struct {
	cwd       string
	transport string
}

func cmdServiceInstall(args []string) {
	opts := parseServiceInstallArgs(args)
	path, err := installService(opts)
	if err != nil {
		fatal("service install failed: %v", err)
	}
	fmt.Printf("Service installed at %s\n", path)
}

func cmdServiceUninstall() {
	path, err := uninstallService()
	if err != nil {
		fatal("service uninstall failed: %v", err)
	}
	fmt.Printf("Service removed from %s\n", path)
}

func parseServiceInstallArgs(args []string) serviceInstallOptions {
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	fs.SetOutput(ioDiscard{})
	cwd := fs.String("cwd", "", "")
	transport := fs.String("transport", "", "")
	if err := fs.Parse(args); err != nil {
		fatal("usage: moxie service install [--cwd <dir>] [--transport <telegram|slack>]")
	}
	if len(fs.Args()) > 0 {
		fatal("unexpected service install args: %s", strings.Join(fs.Args(), " "))
	}
	opts := serviceInstallOptions{
		cwd:       strings.TrimSpace(*cwd),
		transport: strings.TrimSpace(*transport),
	}
	if opts.transport != "" && opts.transport != "telegram" && opts.transport != "slack" {
		fatal("unsupported transport for service install: %s", opts.transport)
	}
	if opts.cwd != "" {
		resolved, err := resolveOrCreateDir(opts.cwd)
		if err != nil {
			fatal("invalid --cwd: %v", err)
		}
		opts.cwd = resolved
	}
	return opts
}

func installService(opts serviceInstallOptions) (string, error) {
	switch runtime.GOOS {
	case "linux":
		return installSystemdService(opts)
	case "darwin":
		return installLaunchdService(opts)
	default:
		return "", fmt.Errorf("service install is not implemented for %s", runtime.GOOS)
	}
}

func uninstallService() (string, error) {
	switch runtime.GOOS {
	case "linux":
		return uninstallSystemdService()
	case "darwin":
		return uninstallLaunchdService()
	default:
		return "", fmt.Errorf("service uninstall is not implemented for %s", runtime.GOOS)
	}
}

func installSystemdService(opts serviceInstallOptions) (string, error) {
	path := systemdServicePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	workingDir, err := serviceInstallWorkingDirectory(opts)
	if err != nil {
		return "", err
	}
	content, err := systemdUnitContents(currentBinaryPath(), opts, workingDir)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	if _, err := runCommand("systemctl", "--user", "daemon-reload"); err != nil {
		return "", err
	}
	return path, nil
}

func uninstallSystemdService() (string, error) {
	path := systemdServicePath()
	_, _ = runCommand("systemctl", "--user", "disable", "--now", defaultServiceUnit)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if _, err := runCommand("systemctl", "--user", "daemon-reload"); err != nil {
		return "", err
	}
	return path, nil
}

func installLaunchdService(opts serviceInstallOptions) (string, error) {
	path := launchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(launchdLogPath()), 0o755); err != nil {
		return "", err
	}
	workingDir, err := serviceInstallWorkingDirectory(opts)
	if err != nil {
		return "", err
	}
	content, err := launchdPlistContents(currentBinaryPath(), opts, workingDir)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func uninstallLaunchdService() (string, error) {
	path := launchdPlistPath()
	_, _ = runCommand("launchctl", "bootout", launchdDomainTarget(os.Getuid()), path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}

func systemdServicePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal("resolve home dir: %v", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", defaultServiceUnit)
}

func currentBinaryPath() string {
	path, err := exec.LookPath("moxie")
	if err == nil && path != "" {
		return path
	}
	exe, err := os.Executable()
	if err == nil && exe != "" {
		return exe
	}
	fatal("failed to locate moxie binary: %v", err)
	return ""
}

func serviceInstallWorkingDirectory(opts serviceInstallOptions) (string, error) {
	if strings.TrimSpace(opts.cwd) != "" {
		return resolveOrCreateDir(opts.cwd)
	}
	cfg, err := store.LoadConfig()
	if err == nil {
		return configuredOrPlatformDefaultCWD(cfg)
	}
	path, err := platformDefaultWorkspaceDir()
	if err != nil {
		return "", err
	}
	return resolveOrCreateDir(path)
}

func serviceCommandArgs(opts serviceInstallOptions) []string {
	args := []string{"serve"}
	if opts.transport != "" {
		args = append(args, "--transport", opts.transport)
	}
	return args
}

func systemdUnitContents(binaryPath string, opts serviceInstallOptions, workingDir string) (string, error) {
	args := append([]string{binaryPath}, serviceCommandArgs(opts)...)
	var quoted []string
	for _, arg := range args {
		quoted = append(quoted, quoteSystemdArg(arg))
	}
	env := serviceEnvironment()
	unit := `[Unit]
Description=Moxie chat agent
After=network-online.target
Wants=network-online.target

[Service]
WorkingDirectory=` + quoteSystemdArg(workingDir) + `
ExecStart=` + strings.Join(quoted, " ") + `
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=5
SuccessExitStatus=143 SIGTERM
TimeoutStopSec=90
KillMode=mixed
Environment=PATH=` + env["PATH"] + `
Environment=HOME=` + env["HOME"] + `

[Install]
WantedBy=default.target
`
	return unit, nil
}

func launchdPlistContents(binaryPath string, opts serviceInstallOptions, workingDir string) (string, error) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString(`  <key>Label</key>` + "\n")
	b.WriteString(`  <string>` + xmlEscape(defaultLaunchdLabel) + `</string>` + "\n")
	b.WriteString(`  <key>ProgramArguments</key>` + "\n")
	b.WriteString("  <array>\n")
	for _, arg := range append([]string{binaryPath}, serviceCommandArgs(opts)...) {
		b.WriteString(`    <string>` + xmlEscape(arg) + `</string>` + "\n")
	}
	b.WriteString("  </array>\n")
	b.WriteString(`  <key>WorkingDirectory</key>` + "\n")
	b.WriteString(`  <string>` + xmlEscape(workingDir) + `</string>` + "\n")
	logPath := launchdLogPath()
	env := serviceEnvironment()
	b.WriteString(`  <key>RunAtLoad</key>` + "\n  <true/>\n")
	b.WriteString(`  <key>KeepAlive</key>` + "\n  <true/>\n")
	b.WriteString(`  <key>EnvironmentVariables</key>` + "\n")
	b.WriteString("  <dict>\n")
	for _, key := range []string{"PATH", "HOME"} {
		if env[key] == "" {
			continue
		}
		b.WriteString(`    <key>` + key + `</key>` + "\n")
		b.WriteString(`    <string>` + xmlEscape(env[key]) + `</string>` + "\n")
	}
	b.WriteString("  </dict>\n")
	b.WriteString(`  <key>StandardOutPath</key>` + "\n")
	b.WriteString(`  <string>` + xmlEscape(logPath) + `</string>` + "\n")
	b.WriteString(`  <key>StandardErrorPath</key>` + "\n")
	b.WriteString(`  <string>` + xmlEscape(logPath) + `</string>` + "\n")
	b.WriteString(`</dict>` + "\n</plist>\n")
	return b.String(), nil
}

func launchdLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal("resolve home dir: %v", err)
	}
	return filepath.Join(home, "Library", "Logs", "moxie.log")
}

func serviceEnvironment() map[string]string {
	env := map[string]string{
		"PATH": os.Getenv("PATH"),
		"HOME": os.Getenv("HOME"),
	}
	if strings.TrimSpace(env["PATH"]) == "" {
		env["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	}
	return env
}

func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		fatal("xml escape failed: %v", err)
	}
	return b.String()
}

func quoteSystemdArg(arg string) string {
	if strings.ContainsAny(arg, " \t\"\\") {
		return strconvQuote(arg)
	}
	return arg
}

func runCommand(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			err = fmt.Errorf("%w\n%s", err, msg)
		}
		if isSystemdBusError(msg) {
			err = fmt.Errorf("%w\n\nHint: the systemd user session is not available.\nRun: sudo loginctl enable-linger %s\nThen retry: moxie service install", err, currentUsername())
		}
	}
	return out, err
}

func isSystemdBusError(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "no medium found") ||
		strings.Contains(lower, "failed to connect to bus") ||
		strings.Contains(lower, "no such file or directory") && strings.Contains(lower, "/run/user/")
}

func currentUsername() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "<username>"
}

func promptRequiredLine(reader *bufio.Reader, label string) string {
	for {
		v := promptLine(reader, label, "")
		if v != "" {
			return v
		}
		fmt.Println("Value required.")
	}
}

func promptLine(reader *bufio.Reader, label, defaultValue string) string {
	fmt.Print(label)
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		fatal("failed to read input: %v", err)
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return defaultValue
	}
	return trimmed
}

func promptYesNo(reader *bufio.Reader, label string, defaultYes bool) bool {
	answer := strings.ToLower(promptLine(reader, label, ""))
	if answer == "" {
		return defaultYes
	}
	return answer == "y" || answer == "yes"
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func strconvQuote(s string) string {
	return fmt.Sprintf("%q", s)
}
