package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	botpkg "github.com/1broseidon/moxie/internal/bot"
	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/prompt"
	"github.com/1broseidon/moxie/internal/scheduler"
	slackpkg "github.com/1broseidon/moxie/internal/slack"
	"github.com/1broseidon/moxie/internal/store"
	webexpkg "github.com/1broseidon/moxie/internal/webex"
	"github.com/1broseidon/oneagent"
)

type serveFlags struct {
	cwd       string
	transport string
}

func parseServeTransportAndCWD() serveFlags {
	flags := serveFlags{}
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--cwd" && i+1 < len(os.Args) {
			flags.cwd = os.Args[i+1]
			i++
			continue
		}
		if os.Args[i] == "--transport" && i+1 < len(os.Args) {
			flags.transport = strings.TrimSpace(os.Args[i+1])
			i++
		}
	}
	return flags
}

func chooseServeTransport(cfg store.Config, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		switch requested {
		case "telegram":
			if _, err := cfg.Telegram(); err != nil {
				return "", err
			}
			return requested, nil
		case "slack":
			if _, err := cfg.Slack(); err != nil {
				return "", err
			}
			return requested, nil
		case "webex":
			if _, err := cfg.Webex(); err != nil {
				return "", err
			}
			return requested, nil
		default:
			return "", fmt.Errorf("unknown transport: %s", requested)
		}
	}

	hasTelegram := false
	if _, err := cfg.Telegram(); err == nil {
		hasTelegram = true
	}
	hasSlack := false
	if _, err := cfg.Slack(); err == nil {
		hasSlack = true
	}
	hasWebex := false
	if _, err := cfg.Webex(); err == nil {
		hasWebex = true
	}

	count := 0
	selected := ""
	if hasTelegram {
		count++
		selected = "telegram"
	}
	if hasSlack {
		count++
		selected = "slack"
	}
	if hasWebex {
		count++
		selected = "webex"
	}

	switch count {
	case 0:
		return "", fmt.Errorf("no valid transport configured")
	case 1:
		return selected, nil
	default:
		return "", fmt.Errorf("multiple transports configured; use --transport telegram, --transport slack, or --transport webex")
	}
}

func chooseServeTransports(cfg store.Config, requested string) ([]string, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		transport, err := chooseServeTransport(cfg, requested)
		if err != nil {
			return nil, err
		}
		return []string{transport}, nil
	}

	transports := make([]string, 0, 3)
	if _, err := cfg.Telegram(); err == nil {
		transports = append(transports, "telegram")
	}
	if _, err := cfg.Slack(); err == nil {
		transports = append(transports, "slack")
	}
	if _, err := cfg.Webex(); err == nil {
		transports = append(transports, "webex")
	}
	if len(transports) == 0 {
		return nil, fmt.Errorf("no valid transport configured")
	}
	return transports, nil
}

func cloneBackends(backends map[string]oneagent.Backend) map[string]oneagent.Backend {
	cloned := make(map[string]oneagent.Backend, len(backends))
	for name, backend := range backends {
		cloned[name] = backend
	}
	return cloned
}

func newTelegramClient(backends map[string]oneagent.Backend) *oneagent.Client {
	cloned := cloneBackends(backends)
	botpkg.ApplySystemPrompt(cloned)
	return &oneagent.Client{Backends: cloned}
}

func newSlackClient(backends map[string]oneagent.Backend) *oneagent.Client {
	cloned := cloneBackends(backends)
	slackpkg.ApplySystemPrompt(cloned)
	return &oneagent.Client{Backends: cloned}
}

func newWebexClient(backends map[string]oneagent.Backend) *oneagent.Client {
	cloned := cloneBackends(backends)
	webexpkg.ApplySystemPrompt(cloned)
	return &oneagent.Client{Backends: cloned}
}

type serveTransportRuntime struct {
	name string
	run  func(context.Context) error
}

type serveTransportResult struct {
	name string
	err  error
}

type serveSignalAction int

const (
	serveSignalNone serveSignalAction = iota
	serveSignalStop
	serveSignalReload
)

func runServeSupervisor(ctx context.Context, transports []serveTransportRuntime) error {
	if len(transports) == 0 {
		return fmt.Errorf("no serve transports configured")
	}

	results := make(chan serveTransportResult, len(transports))
	for _, transport := range transports {
		transport := transport
		go func() {
			log.Printf("starting %s transport", transport.name)
			results <- serveTransportResult{name: transport.name, err: transport.run(ctx)}
		}()
	}

	failures := 0
	remaining := len(transports)
	for remaining > 0 {
		result := <-results
		remaining--
		switch {
		case result.err != nil && ctx.Err() == nil && !dispatch.IsShuttingDown():
			failures++
			log.Printf("%s transport exited with error: %v", result.name, result.err)
		case result.err != nil:
			log.Printf("%s transport stopped: %v", result.name, result.err)
		default:
			log.Printf("%s transport stopped", result.name)
		}
	}

	if failures == len(transports) {
		return fmt.Errorf("all configured transports failed")
	}
	return nil
}

func cmdServe() {
	cleanup := acquireServeLock()
	defer cleanup()

	flags := parseServeTransportAndCWD()

	for {
		action, err := runServeOnce(flags.transport, flags.cwd)
		if action == serveSignalReload {
			if err != nil {
				log.Printf("reload completed after transport stop: %v", err)
			}
			log.Println("reload requested; reloading config and restarting transports")
			continue
		}
		if err != nil {
			fatal("%v", err)
		}
		return
	}
}

func runServeOnce(requestedTransport, requestedCWD string) (serveSignalAction, error) {
	cfg, err := store.LoadConfig()
	if err != nil {
		return serveSignalNone, err
	}

	defaultCWD, err := resolveServeDefaultCWD(cfg, requestedCWD)
	if err != nil {
		return serveSignalNone, fmt.Errorf("resolve default workspace: %w", err)
	}

	backends, err := loadServeBackends()
	if err != nil {
		return serveSignalNone, fmt.Errorf("no backends: %w", err)
	}

	// Set the default backend to the first installed one, so fresh
	// conversations and schedules don't silently target a missing CLI.
	if resolved := resolveDefaultBackend(backends); resolved != "" {
		store.SetDefaultBackend(resolved)
		log.Printf("default backend: %s", resolved)
	}

	// Warm whisper detection in the background so voice message metadata is
	// ready before the first audio message arrives.
	go prompt.WarmWhisper()

	schedules := newScheduleStore()
	if err := schedules.Repair(store.JobExists); err != nil {
		log.Printf("schedule repair failed: %v", err)
	}
	transports, err := chooseServeTransports(cfg, requestedTransport)
	if err != nil {
		return serveSignalNone, err
	}

	dispatch.SetShuttingDown(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actionCh, stopSignals := installServeSignalHandler(cancel)
	defer stopSignals()

	runtimes := make([]serveTransportRuntime, 0, len(transports))
	for _, transport := range transports {
		switch transport {
		case "telegram":
			client := newTelegramClient(backends)
			runtimes = append(runtimes, serveTransportRuntime{
				name: "telegram",
				run: func(ctx context.Context) error {
					return runTelegramTransport(ctx, cfg, defaultCWD, client, schedules)
				},
			})
		case "slack":
			client := newSlackClient(backends)
			runtimes = append(runtimes, serveTransportRuntime{
				name: "slack",
				run: func(ctx context.Context) error {
					return runSlackTransport(ctx, cfg, defaultCWD, client, schedules)
				},
			})
		case "webex":
			client := newWebexClient(backends)
			runtimes = append(runtimes, serveTransportRuntime{
				name: "webex",
				run: func(ctx context.Context) error {
					return runWebexTransport(ctx, cfg, defaultCWD, client, schedules)
				},
			})
		default:
			fatal("unsupported transport: %s", transport)
		}
	}

	startSubagentWatcher(ctx, cfg, backends, schedules)
	startWorkflowWatcher(ctx, cfg, backends, schedules)

	err = runServeSupervisor(ctx, runtimes)
	return serveSignalActionFromChannel(actionCh), err
}

func loadServeBackends() (map[string]oneagent.Backend, error) {
	return oneagent.LoadBackendsWithOptions(oneagent.LoadOptions{
		IncludeEmbedded: true,
		OverridePath:    store.ConfigFile("backends.json"),
	})
}

// resolveDefaultBackend returns the name of the first installed backend,
// preferring common names in a stable order. Returns "" if none are found.
func resolveDefaultBackend(backends map[string]oneagent.Backend) string {
	// Preferred order — check common backends first for a predictable default.
	preferred := []string{"claude", "pi", "codex", "opencode", "gemini"}
	for _, name := range preferred {
		if b, ok := backends[name]; ok {
			if _, found := oneagent.ResolveBackendProgram(b); found {
				return name
			}
		}
	}
	// Fall back to any installed backend.
	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, found := oneagent.ResolveBackendProgram(backends[name]); found {
			return name
		}
	}
	return ""
}

func availableBackendNames(backends map[string]oneagent.Backend) string {
	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func installServeSignalHandler(cancel func()) (<-chan serveSignalAction, func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	actions := make(chan serveSignalAction, 1)
	stop := make(chan struct{})
	go func() {
		defer close(actions)
		select {
		case s := <-sig:
			dispatch.SetShuttingDown(true)
			if cancel != nil {
				cancel()
			}
			switch s {
			case syscall.SIGHUP:
				log.Println("reload requested; draining current work")
				actions <- serveSignalReload
			default:
				log.Println("shutdown requested; draining current work")
				actions <- serveSignalStop
			}
		case <-stop:
			return
		}
	}()
	return actions, func() {
		signal.Stop(sig)
		close(stop)
	}
}

func serveSignalActionFromChannel(actionCh <-chan serveSignalAction) serveSignalAction {
	select {
	case action, ok := <-actionCh:
		if !ok {
			return serveSignalNone
		}
		return action
	default:
		return serveSignalNone
	}
}

// --- Per-transport serve runners ---

func runTelegramTransport(ctx context.Context, cfg store.Config, defaultCWD string, client *oneagent.Client, schedules *scheduler.Store) error {
	bot, err := botpkg.NewBot(cfg)
	if err != nil {
		return fmt.Errorf("bot init failed: %w", err)
	}

	botpkg.RecoverPendingJobs(bot, client, schedules)
	if botpkg.ReadCursor() == 0 {
		botpkg.SeedCursor(bot, getUpdates)
	}
	botpkg.RegisterCommands(bot)
	conversation := botpkg.ConfigConversation(cfg)
	startScheduleLoopTelegram(ctx, bot, client, schedules, conversation.ID())
	startDeliveryRetryLoopTelegram(ctx, bot, client, schedules)

	cursor := botpkg.ReadCursor()
	st := store.ReadConversationState(conversation.ID())
	log.Printf("telegram transport ready — conversation=%s backend=%s thread=%s cwd=%s", conversation.ID(), st.Backend, st.ThreadID, defaultCWD)

	offset := func() int {
		if cursor > 0 {
			return cursor + 1
		}
		return 0
	}

	for ctx.Err() == nil && !dispatch.IsShuttingDown() {
		for _, u := range getUpdates(bot, offset(), 30) {
			if ctx.Err() != nil || dispatch.IsShuttingDown() {
				return nil
			}
			st = store.ReadConversationState(conversation.ID())
			botpkg.HandleUpdate(u, bot, &cfg, defaultCWD, st, client)
			cursor = u.ID
			botpkg.WriteCursor(cursor)
			if ctx.Err() != nil || dispatch.IsShuttingDown() {
				return nil
			}
		}
	}
	return nil
}

func slackDefaultConversation(cfg store.Config) chat.ConversationRef {
	ch, err := cfg.Slack()
	if err != nil {
		return chat.ConversationRef{Provider: chat.ProviderSlack}
	}
	return chat.ConversationRef{
		Provider:  chat.ProviderSlack,
		ChannelID: ch.ChannelID,
	}
}

func runSlackTransport(ctx context.Context, cfg store.Config, defaultCWD string, client *oneagent.Client, schedules *scheduler.Store) error {
	adapter, err := slackpkg.New(&cfg, defaultCWD, client, schedules)
	if err != nil {
		return fmt.Errorf("slack init failed: %w", err)
	}

	slackpkg.RecoverPendingJobs(adapter.API(), client, schedules)
	defaultConversation := slackDefaultConversation(cfg)
	if defaultConversation.ChannelID != "" {
		startScheduleLoopSlack(ctx, adapter.API(), client, schedules, defaultConversation.ID())
	}
	startDeliveryRetryLoopSlack(ctx, adapter.API(), client, schedules)

	st := store.ReadConversationState(defaultConversation.ID())
	log.Printf("slack transport ready — conversation=%s backend=%s thread=%s cwd=%s", defaultConversation.ID(), st.Backend, st.ThreadID, defaultCWD)

	if err := adapter.Run(ctx); err != nil && ctx.Err() == nil && !dispatch.IsShuttingDown() {
		return fmt.Errorf("slack serve failed: %w", err)
	}
	return nil
}

func webexDefaultConversation(cfg store.Config) chat.ConversationRef {
	ch, err := cfg.Webex()
	if err != nil {
		return chat.ConversationRef{Provider: chat.ProviderWebex}
	}
	return chat.ConversationRef{
		Provider:  chat.ProviderWebex,
		ChannelID: ch.ChannelID,
	}
}

func runWebexTransport(ctx context.Context, cfg store.Config, defaultCWD string, client *oneagent.Client, schedules *scheduler.Store) error {
	adapter, err := webexpkg.New(&cfg, defaultCWD, client, schedules)
	if err != nil {
		return fmt.Errorf("webex init failed: %w", err)
	}

	webexpkg.RecoverPendingJobs(adapter.API(), client, schedules)
	defaultConversation := webexDefaultConversation(cfg)
	if defaultConversation.ChannelID != "" {
		startScheduleLoopWebex(ctx, adapter.API(), client, schedules, defaultConversation.ID())
	}
	startDeliveryRetryLoopWebex(ctx, adapter.API(), client, schedules)

	st := store.ReadConversationState(defaultConversation.ID())
	log.Printf("webex transport ready — conversation=%s backend=%s thread=%s cwd=%s (direct-message only)", defaultConversation.ID(), st.Backend, st.ThreadID, defaultCWD)

	if err := adapter.Run(ctx); err != nil && ctx.Err() == nil && !dispatch.IsShuttingDown() {
		return fmt.Errorf("webex serve failed: %w", err)
	}
	return nil
}
