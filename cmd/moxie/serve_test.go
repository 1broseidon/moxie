package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

func TestChooseServeTransportsReturnsAllConfigured(t *testing.T) {
	cfg := store.Config{
		Channels: map[string]store.ChannelConfig{
			"telegram": {
				Provider:  "telegram",
				Token:     "tg-token",
				ChannelID: "123",
			},
			"slack": {
				Provider:  "slack",
				Token:     "xoxb-token",
				AppToken:  "xapp-token",
				ChannelID: "C123",
			},
		},
	}

	got, err := chooseServeTransports(cfg, "")
	if err != nil {
		t.Fatalf("chooseServeTransports() err = %v", err)
	}
	if len(got) != 2 || got[0] != "telegram" || got[1] != "slack" {
		t.Fatalf("chooseServeTransports() = %v, want [telegram slack]", got)
	}
}

func TestRunServeSupervisorKeepsHealthyTransportRunningAfterPeerFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	healthyStarted := make(chan struct{})
	healthyStopped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runServeSupervisor(ctx, []serveTransportRuntime{
			{
				name: "broken",
				run: func(context.Context) error {
					return errors.New("boom")
				},
			},
			{
				name: "healthy",
				run: func(ctx context.Context) error {
					close(healthyStarted)
					<-ctx.Done()
					close(healthyStopped)
					return nil
				},
			},
		})
	}()

	select {
	case <-healthyStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("healthy transport did not start")
	}

	select {
	case err := <-done:
		t.Fatalf("supervisor exited early: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	cancel()

	select {
	case <-healthyStopped:
	case <-time.After(2 * time.Second):
		t.Fatal("healthy transport did not stop after cancellation")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServeSupervisor() err = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not finish after cancellation")
	}
}

func TestRunServeSupervisorFailsWhenAllTransportsFail(t *testing.T) {
	err := runServeSupervisor(context.Background(), []serveTransportRuntime{
		{
			name: "telegram",
			run: func(context.Context) error {
				return errors.New("telegram down")
			},
		},
		{
			name: "slack",
			run: func(context.Context) error {
				return errors.New("slack down")
			},
		},
	})
	if err == nil {
		t.Fatal("expected supervisor error when all transports fail")
	}
}

func TestInstallServeSignalHandlerReloadsOnSIGHUP(t *testing.T) {
	dispatch.SetShuttingDown(false)
	t.Cleanup(func() { dispatch.SetShuttingDown(false) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actionCh, stop := installServeSignalHandler(cancel)
	defer stop()

	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(): %v", err)
	}
	if err := p.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("Signal(SIGHUP): %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("context was not canceled on reload signal")
	}

	select {
	case got := <-actionCh:
		if got != serveSignalReload {
			t.Fatalf("signal action = %v, want reload", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload action not delivered")
	}
}

func TestInstallServeSignalHandlerStopsOnSIGTERM(t *testing.T) {
	dispatch.SetShuttingDown(false)
	t.Cleanup(func() { dispatch.SetShuttingDown(false) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actionCh, stop := installServeSignalHandler(cancel)
	defer stop()

	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(): %v", err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal(SIGTERM): %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("context was not canceled on stop signal")
	}

	select {
	case got := <-actionCh:
		if got != serveSignalStop {
			t.Fatalf("signal action = %v, want stop", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop action not delivered")
	}
}

func TestLaunchdTargets(t *testing.T) {
	if got := launchdDomainTarget(501); got != "gui/501" {
		t.Fatalf("launchdDomainTarget() = %q, want gui/501", got)
	}
	if got := launchdServiceTarget(501); got != "gui/501/"+defaultLaunchdLabel {
		t.Fatalf("launchdServiceTarget() = %q", got)
	}
}

func TestParseLaunchdPID(t *testing.T) {
	out := `
gui/501/io.github.1broseidon.moxie = {
	pid = 12345
	last exit code = 0
}`
	pid, ok := parseLaunchdPID(out)
	if !ok {
		t.Fatal("parseLaunchdPID() ok = false, want true")
	}
	if pid != 12345 {
		t.Fatalf("parseLaunchdPID() = %d, want 12345", pid)
	}
}

func TestDispatchSynthesisPreservesParentStateAndReplyConversation(t *testing.T) {
	restoreStore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restoreStore)

	store.WriteConversationState("slack:C123", store.State{
		Backend:  "pi",
		Model:    "small",
		ThreadID: "wrong-thread",
	})

	restoreRun := dispatch.SetRunModelFuncForTest(func(job *store.PendingJob, _ *oneagent.Client, _ func(string)) (string, bool) {
		if job.Source != "subagent-synthesis" {
			t.Fatalf("job source = %q, want subagent-synthesis", job.Source)
		}
		if got := job.State; got != (store.State{
			Backend:  "claude",
			Model:    "sonnet",
			ThreadID: "parent-thread",
		}) {
			t.Fatalf("synthesis state = %+v, want preserved parent state", got)
		}
		if job.ReplyConversation != "slack:C123:1710.9" {
			t.Fatalf("reply conversation = %q, want original Slack reply thread", job.ReplyConversation)
		}
		return "synthesized", false
	})
	t.Cleanup(restoreRun)

	subJob := store.PendingJob{
		ID:                "job-sub",
		ConversationID:    "slack:C123",
		ReplyConversation: "slack:C123:1710.9",
		DelegatedTask:     "inspect logs",
		DelegationContext: "user asked about a crash",
		State: store.State{
			Backend:  "pi",
			Model:    "small",
			ThreadID: "sub-thread",
		},
		SynthesisState: store.State{
			Backend:  "claude",
			Model:    "sonnet",
			ThreadID: "parent-thread",
		},
	}

	var delivered store.PendingJob
	if err := dispatchSynthesis(subJob, "worker result", nil, nil, func(job store.PendingJob) error {
		delivered = job
		return nil
	}); err != nil {
		t.Fatalf("dispatchSynthesis(): %v", err)
	}

	if delivered.Result != "synthesized" {
		t.Fatalf("delivered result = %q, want synthesized", delivered.Result)
	}
	if delivered.ReplyConversation != "slack:C123:1710.9" {
		t.Fatalf("delivered reply conversation = %q, want original reply target", delivered.ReplyConversation)
	}
}

func TestLoadServeBackendsUsesMoxieOverridePath(t *testing.T) {
	restoreStore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restoreStore)

	override := `{
		"pi": {
			"run": "pi-custom {prompt}",
			"format": "json",
			"result": "result",
			"session": "session"
		},
		"custom": {
			"run": "custom-agent {prompt}",
			"format": "json",
			"result": "result",
			"session": "session"
		}
	}`
	if err := os.WriteFile(store.ConfigFile("backends.json"), []byte(override), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}

	backends, err := loadServeBackends()
	if err != nil {
		t.Fatalf("loadServeBackends(): %v", err)
	}

	if got := backends["pi"].Cmd[0]; got != "pi-custom" {
		t.Fatalf("pi override not applied, cmd[0] = %q", got)
	}
	if _, ok := backends["custom"]; !ok {
		t.Fatalf("custom backend missing from loaded map")
	}
	if _, ok := backends["claude"]; !ok {
		t.Fatal("embedded defaults should remain available")
	}
}
