package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/1broseidon/moxie/internal/store"
)

func resultUsage() {
	fmt.Println(`moxie result — retrieve metadata and results from previous subagent runs

Usage:
  moxie result list [--limit <n>]
  moxie result show <id>
  moxie result search <query>

Subcommands:
  list              List recent subagent artifacts (default last 20)
  show <id>         Show artifact metadata and the thread ID containing the full result
  search <query>    Search artifact tasks for a substring

Flags for list:
  --limit <n>       Number of artifacts to show (default 20)

When to use:
  Use to find or reference the output of a previous subagent run
  Every completed subagent produces a lightweight artifact with metadata
  The artifact links to the thread where the full conversation is stored
  Use search to find a specific result without knowing the artifact ID

Examples:
  moxie result list
  moxie result list --limit 5
  moxie result show art-1773971234567890
  moxie result search "security audit"`)
}

func cmdResult() {
	if len(os.Args) < 3 {
		resultUsage()
		return
	}
	sub := os.Args[2]
	if sub == "help" || sub == "--help" || sub == "-h" {
		resultUsage()
		return
	}
	switch sub {
	case "list":
		cmdResultList(os.Args[3:])
	case "show":
		cmdResultShow(os.Args[3:])
	case "search":
		cmdResultSearch(os.Args[3:])
	default:
		resultUsage()
	}
}

func cmdResultList(args []string) {
	limit := 20
	for i := 0; i < len(args); i++ {
		if args[i] == "--limit" && i+1 < len(args) {
			if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
				limit = n
			}
			i++
		}
	}
	artifacts := store.ListArtifacts()
	if len(artifacts) == 0 {
		fmt.Println("No artifacts.")
		return
	}
	if len(artifacts) > limit {
		artifacts = artifacts[:limit]
	}
	for _, a := range artifacts {
		task := a.Task
		if len(task) > 80 {
			task = task[:80] + "..."
		}
		fmt.Printf("%s  %-8s  %s  %s\n", a.ID, a.Backend, a.Created.Format("2006-01-02 15:04"), task)
	}
}

func cmdResultShow(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: moxie result show <id>")
		os.Exit(1)
	}
	a, ok := store.ReadArtifact(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "artifact not found: %s\n", args[0])
		os.Exit(1)
	}
	fmt.Printf("ID:        %s\n", a.ID)
	fmt.Printf("Job:       %s\n", a.JobID)
	fmt.Printf("Backend:   %s\n", a.Backend)
	fmt.Printf("Thread:    %s\n", a.ThreadID)
	if a.ParentJob != "" {
		fmt.Printf("Parent:    %s\n", a.ParentJob)
	}
	fmt.Printf("Created:   %s\n", a.Created.Format(time.RFC3339))
	fmt.Printf("Task:      %s\n", a.Task)
}

func cmdResultSearch(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: moxie result search <query>")
		os.Exit(1)
	}
	query := strings.ToLower(strings.Join(args, " "))
	artifacts := store.ListArtifacts()
	found := false
	for _, a := range artifacts {
		if strings.Contains(strings.ToLower(a.Task), query) {
			task := a.Task
			if len(task) > 80 {
				task = task[:80] + "..."
			}
			fmt.Printf("%s  %-8s  %s  %s\n", a.ID, a.Backend, a.Created.Format("2006-01-02 15:04"), task)
			found = true
		}
	}
	if !found {
		fmt.Println("No matching artifacts.")
	}
}
