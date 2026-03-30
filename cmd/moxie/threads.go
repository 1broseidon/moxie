package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/1broseidon/oneagent"
)

func cmdThreads() {
	if len(os.Args) < 4 || os.Args[2] != "show" {
		fmt.Fprintln(os.Stderr, "usage: moxie threads show <id>")
		os.Exit(1)
	}
	id := strings.TrimSpace(os.Args[3])
	thread, err := oneagent.LoadThread(id)
	if err != nil {
		fatal("load thread: %v", err)
	}
	if thread.Summary != "" {
		fmt.Printf("Summary: %s\n\n", thread.Summary)
	}
	if len(thread.NativeSessions) > 0 {
		fmt.Print("Sessions:")
		for backend, sid := range thread.NativeSessions {
			fmt.Printf("  %s=%s", backend, sid)
		}
		fmt.Println()
		fmt.Println()
	}
	if len(thread.Turns) == 0 {
		fmt.Println("no turns")
		return
	}
	for _, t := range thread.Turns {
		ts := t.TS
		if parsed, err := time.Parse(time.RFC3339, t.TS); err == nil {
			ts = parsed.Local().Format("Jan 2 3:04pm")
		}
		fmt.Printf("[%s] %s (%s): %s\n", ts, t.Role, t.Backend, t.Content)
	}
}
