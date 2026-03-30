package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

const defaultServiceUnit = "moxie-serve.service"
const defaultLaunchdLabel = "io.github.1broseidon.moxie"

var launchdReloadSignal os.Signal = syscall.Signal(1)

func serviceUsage() {
	fmt.Println(`moxie service — control the background service

Usage:
  moxie service install [--cwd <dir>] [--transport <telegram|slack|webex>]
  moxie service uninstall
  moxie service start
  moxie service stop
  moxie service restart
  moxie service reload
  moxie service status

Notes:
  Linux uses systemd user services
  macOS uses launchd with ~/Library/LaunchAgents/io.github.1broseidon.moxie.plist`)
}

func cmdService() {
	if len(os.Args) < 3 {
		serviceUsage()
		return
	}
	switch os.Args[2] {
	case "install":
		cmdServiceInstall(os.Args[3:])
	case "uninstall":
		cmdServiceUninstall()
	case "start", "stop", "restart", "reload", "status":
		cmdServiceControl(os.Args[2])
	default:
		serviceUsage()
	}
}

func cmdServiceControl(action string) {
	switch runtime.GOOS {
	case "linux":
		runSystemdUserAction(action)
	case "darwin":
		runLaunchdUserAction(action)
	default:
		fatal("moxie service %s is not implemented for %s yet; use the platform service manager directly", action, runtime.GOOS)
	}
	if msg := serviceSuccessMessage(action); msg != "" {
		fmt.Println(msg)
	}
}

func serviceSuccessMessage(action string) string {
	switch action {
	case "start":
		return "Service started"
	case "stop":
		return "Service stopped"
	case "restart":
		return "Service restarted"
	case "reload":
		return "Service reloaded"
	default:
		return ""
	}
}

func runSystemdUserAction(action string) {
	cmd := exec.Command("systemctl", "--user", action, defaultServiceUnit)
	cmd.Stdin = os.Stdin
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		_, _ = os.Stdout.Write(out)
	}
	if err != nil {
		output := strings.TrimSpace(string(out))
		if isSystemdBusError(output) {
			fatal("systemctl --user %s %s failed: %v\n\nThe systemd user session is not available.\nRun: sudo loginctl enable-linger %s\nThen retry: moxie service %s", action, defaultServiceUnit, err, currentUsername(), action)
		}
		fatal("systemctl --user %s %s failed: %v", action, defaultServiceUnit, err)
	}
}

func runLaunchdUserAction(action string) {
	plist := launchdPlistPath()
	target := launchdServiceTarget(os.Getuid())
	domain := launchdDomainTarget(os.Getuid())

	switch action {
	case "start":
		requireLaunchdPlist(plist)
		if launchdServiceLoaded(target) {
			runLaunchctl("kickstart", target)
			return
		}
		runLaunchctl("bootstrap", domain, plist)
	case "stop":
		requireLaunchdPlist(plist)
		runLaunchctl("bootout", domain, plist)
	case "restart":
		requireLaunchdPlist(plist)
		if launchdServiceLoaded(target) {
			runLaunchctl("kickstart", "-k", target)
			return
		}
		runLaunchctl("bootstrap", domain, plist)
	case "reload":
		pid, err := launchdServicePID(target)
		if err != nil {
			fatal("%v", err)
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			fatal("failed to resolve %s (pid %d): %v", target, pid, err)
		}
		if err := proc.Signal(launchdReloadSignal); err != nil {
			fatal("failed to signal %s (pid %d): %v", target, pid, err)
		}
	case "status":
		runLaunchctl("print", target)
	default:
		fatal("unsupported service action: %s", action)
	}
}

func launchdPlistPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal("resolve home dir: %v", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", defaultLaunchdLabel+".plist")
}

func launchdDomainTarget(uid int) string {
	return fmt.Sprintf("gui/%d", uid)
}

func launchdServiceTarget(uid int) string {
	return launchdDomainTarget(uid) + "/" + defaultLaunchdLabel
}

func requireLaunchdPlist(path string) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	fatal("launchd plist not found: %s\nCreate a LaunchAgent at that path, then rerun moxie service", path)
}

func runLaunchctl(args ...string) {
	cmd := exec.Command("launchctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		fatal("launchctl %s failed: %v", strings.Join(args, " "), err)
	}
}

func launchdServiceLoaded(target string) bool {
	cmd := exec.Command("launchctl", "print", target)
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

func launchdServicePID(target string) (int, error) {
	out, err := exec.Command("launchctl", "print", target).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return 0, fmt.Errorf("launchctl print %s failed: %s", target, msg)
		}
		return 0, fmt.Errorf("launchctl print %s failed: %w", target, err)
	}
	pid, ok := parseLaunchdPID(string(out))
	if !ok {
		return 0, fmt.Errorf("could not determine pid for %s", target)
	}
	return pid, nil
}

func parseLaunchdPID(output string) (int, bool) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "pid = ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "pid = "))
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err == nil && pid > 0 {
			return pid, true
		}
	}
	return 0, false
}
