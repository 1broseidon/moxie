package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	botpkg "github.com/1broseidon/moxie/internal/bot"
	"github.com/1broseidon/moxie/internal/store"
	tb "gopkg.in/telebot.v4"
)

func mustConfigAndBot() (store.Config, *tb.Bot) {
	cfg, err := store.LoadConfig()
	if err != nil {
		fatal("%v", err)
	}
	bot, err := botpkg.NewBot(cfg)
	if err != nil {
		fatal("bot init failed: %v", err)
	}
	return cfg, bot
}

func cmdMessages() {
	format, limit := parseListFlags(2)
	_, bot := mustConfigAndBot()
	msgs := extractMessages(getUpdates(bot, -limit, 0))
	if len(msgs) == 0 {
		return
	}
	printMessages(msgs, format)
}

func cmdPoll() {
	format, _ := parseListFlags(2)
	_, bot := mustConfigAndBot()
	msgs := extractMessages(getUpdates(bot, botpkg.CursorOffset(), 0))
	if len(msgs) == 0 {
		return
	}
	maxID := 0
	for _, m := range msgs {
		if m.ID > maxID {
			maxID = m.ID
		}
	}
	botpkg.WriteCursor(maxID)
	printMessages(msgs, format)
}

func cmdCursor() {
	if len(os.Args) >= 3 {
		switch os.Args[2] {
		case "set":
			if len(os.Args) < 4 {
				fatal("usage: moxie cursor set <update_id>")
			}
			n, err := strconv.Atoi(os.Args[3])
			if err != nil {
				fatal("invalid update_id: %s", os.Args[3])
			}
			botpkg.WriteCursor(n)
			fmt.Printf("cursor set to %d\n", n)
			return
		case "reset":
			if err := os.Remove(store.ConfigFile("telegram-cursor")); err != nil && !os.IsNotExist(err) {
				fatal("failed to reset cursor: %v", err)
			}
			fmt.Println("cursor reset")
			return
		}
	}
	c := botpkg.ReadCursor()
	if c == 0 {
		fmt.Println("cursor: not set (will fetch all available)")
	} else {
		fmt.Printf("cursor: %d\n", c)
	}
}

// --- Message display helpers ---

func parseListFlags(startIdx int) (format string, limit int) {
	format = "md"
	limit = 10
	for i := startIdx; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--json":
			format = "json"
		case "--md":
			format = "md"
		case "--raw":
			format = "raw"
		case "-n":
			if i+1 < len(os.Args) {
				n, err := strconv.Atoi(os.Args[i+1])
				if err == nil {
					limit = n
				}
				i++
			}
		}
	}
	return
}

type msgInfo struct {
	ID   int       `json:"id"`
	From string    `json:"from"`
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

func extractMessages(updates []tb.Update) []msgInfo {
	var msgs []msgInfo
	for _, u := range updates {
		if u.Message == nil {
			continue
		}
		m := u.Message
		msgs = append(msgs, msgInfo{
			ID:   u.ID,
			From: botpkg.SenderName(m.Sender),
			Text: m.Text,
			Time: time.Unix(m.Unixtime, 0),
		})
	}
	return msgs
}

func printMessages(msgs []msgInfo, format string) {
	if len(msgs) == 0 {
		fmt.Println("no messages")
		return
	}

	switch format {
	case "json":
		out, err := json.MarshalIndent(msgs, "", "  ")
		store.Check(err)
		fmt.Println(string(out))
	case "raw":
		for _, m := range msgs {
			fmt.Printf("[%s] %s: %s\n", m.Time.Format("Jan 02 15:04"), m.From, m.Text)
		}
	default:
		for _, m := range msgs {
			fmt.Printf("- **%s** (%s): %s\n", m.From, m.Time.Format("Jan 2 3:04pm"), m.Text)
		}
	}
}

func getUpdates(bot *tb.Bot, offset int, timeout int) []tb.Update {
	params := map[string]string{
		"timeout":         strconv.Itoa(timeout),
		"allowed_updates": `["message"]`,
	}
	if offset != 0 {
		params["offset"] = strconv.Itoa(offset)
	}

	data, err := bot.Raw("getUpdates", params)
	if err != nil {
		if timeout > 0 {
			log.Printf("getUpdates error: %v, retrying in 5s", err)
			time.Sleep(5 * time.Second)
			return nil
		}
		fatal("failed to get updates: %v", err)
	}

	var resp struct {
		Result []tb.Update `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		if timeout > 0 {
			log.Printf("parse error: %v", err)
			return nil
		}
		fatal("failed to parse updates: %v", err)
	}
	return resp.Result
}
