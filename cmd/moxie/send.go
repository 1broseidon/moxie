package main

import (
	"fmt"
	"strings"

	botpkg "github.com/1broseidon/moxie/internal/bot"
	slackpkg "github.com/1broseidon/moxie/internal/slack"
	"github.com/1broseidon/moxie/internal/store"
	webexpkg "github.com/1broseidon/moxie/internal/webex"
)

func cmdSend() {
	msg := strings.TrimSpace(joinArgsExcludingTransport(2))
	if msg == "" {
		fatal("usage: moxie send <message>")
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		fatal("%v", err)
	}

	// Rate limit sends to prevent agent-driven chat flooding.
	if err := store.CheckRateLimit("send", cfg.MaxJobsPerMinuteLimit()); err != nil {
		fatal("%v", err)
	}

	transport, err := chooseServeTransport(cfg, parseTransportFlag(2))
	if err != nil {
		fatal("%v", err)
	}

	switch transport {
	case "telegram":
		bot, err := botpkg.NewBot(cfg)
		if err != nil {
			fatal("bot init failed: %v", err)
		}
		jobID, delivered := botpkg.SendImmediate(bot, botpkg.ConfigConversation(cfg), msg)
		if delivered {
			fmt.Println("sent")
			return
		}
		fmt.Printf("queued for retry (job %s)\n", jobID)
	case "slack":
		conversation := slackDefaultConversation(cfg)
		if conversation.ChannelID == "" {
			fatal("slack send requires channels.slack.channel_id")
		}
		adapter, err := slackpkg.New(&cfg, "", nil, nil)
		if err != nil {
			fatal("slack init failed: %v", err)
		}
		jobID, delivered := slackpkg.SendImmediate(adapter.API(), conversation, msg)
		if delivered {
			fmt.Println("sent")
			return
		}
		fmt.Printf("queued for retry (job %s)\n", jobID)
	case "webex":
		conversation := webexDefaultConversation(cfg)
		if conversation.ChannelID == "" {
			fatal("webex send requires channels.webex.channel_id (a 1:1 direct room ID)")
		}
		adapter, err := webexpkg.New(&cfg, "", nil, nil)
		if err != nil {
			fatal("webex init failed: %v", err)
		}
		jobID, delivered := webexpkg.SendImmediate(adapter.API(), conversation, msg)
		if delivered {
			fmt.Println("sent")
			return
		}
		fmt.Printf("queued for retry (job %s)\n", jobID)
	default:
		fatal("unsupported transport: %s", transport)
	}
}
