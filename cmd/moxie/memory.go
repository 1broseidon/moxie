package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/1broseidon/moxie/internal/memory"
)

func memoryUsage() {
	fmt.Println(`moxie memory — query and inspect stored memories

Usage:
  moxie memory recall <query>       Search memories by relevance (FTS5 + vector)
  moxie memory list [--json]        List all stored memories
  moxie memory stats                Show memory count by kind and scope
  moxie memory mode [off|dry-run|on]  Show or set memory mode`)
}

func cmdMemory() {
	if len(os.Args) < 3 {
		memoryUsage()
		return
	}

	switch os.Args[2] {
	case "recall":
		cmdMemoryRecall()
	case "list":
		cmdMemoryList()
	case "stats":
		cmdMemoryStats()
	case "mode":
		cmdMemoryMode()
	default:
		memoryUsage()
	}
}

func cmdMemoryRecall() {
	if len(os.Args) < 4 {
		fatal("usage: moxie memory recall <query>")
	}
	query := strings.Join(os.Args[3:], " ")

	s, err := memory.Open()
	if err != nil {
		fatal("open memory store: %v", err)
	}
	defer s.Close()

	// Generate embedding for semantic search if available.
	var queryEmb []float32
	if embedder := memory.InitEmbedder(); embedder != nil {
		defer embedder.Close()
		if memory.EmbedFunc != nil {
			emb, err := memory.EmbedFunc(query)
			if err == nil {
				queryEmb = emb
			}
		}
	}

	cwd, _ := os.Getwd()
	results, err := s.Search(query, queryEmb, cwd, 10)
	if err != nil {
		fatal("search: %v", err)
	}

	if len(results) == 0 {
		fmt.Println("No matching memories.")
		return
	}

	for _, r := range results {
		tags := ""
		if len(r.Tags) > 0 {
			tags = "[" + strings.Join(r.Tags, ", ") + "] "
		}
		fmt.Printf("- %s%s\n  kind=%s scope=%s score=%.3f\n", tags, r.Text, r.Kind, r.Scope, r.Score)
	}
}

func cmdMemoryList() {
	jsonOutput := len(os.Args) > 3 && os.Args[3] == "--json"

	s, err := memory.Open()
	if err != nil {
		fatal("open memory store: %v", err)
	}
	defer s.Close()

	results, err := s.All()
	if err != nil {
		fatal("list: %v", err)
	}

	if len(results) == 0 {
		fmt.Println("No memories stored.")
		return
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(results)
		return
	}

	for _, r := range results {
		tags := ""
		if len(r.Tags) > 0 {
			tags = "[" + strings.Join(r.Tags, ", ") + "] "
		}
		fmt.Printf("%s  %s%s\n  kind=%s scope=%s project=%s\n", r.CreatedAt.Format("2006-01-02"), tags, r.Text, r.Kind, r.Scope, r.Project)
	}
}

func cmdMemoryStats() {
	s, err := memory.Open()
	if err != nil {
		fatal("open memory store: %v", err)
	}
	defer s.Close()

	stats, err := s.Stats()
	if err != nil {
		fatal("stats: %v", err)
	}

	fmt.Printf("Total: %d memories\n", stats.Total)
	if len(stats.ByKind) > 0 {
		fmt.Println("\nBy kind:")
		for kind, count := range stats.ByKind {
			fmt.Printf("  %-12s %d\n", kind, count)
		}
	}
	if len(stats.ByScope) > 0 {
		fmt.Println("\nBy scope:")
		for scope, count := range stats.ByScope {
			fmt.Printf("  %-12s %d\n", scope, count)
		}
	}
	fmt.Printf("\nMode: %s\n", memory.CurrentMode())
}

func cmdMemoryMode() {
	if len(os.Args) < 4 {
		fmt.Printf("memory_mode: %s\n", memory.CurrentMode())
		return
	}
	mode := memory.Mode(os.Args[3])
	switch mode {
	case memory.ModeOff, memory.ModeDryRun, memory.ModeOn:
		if err := memory.SetMode(mode); err != nil {
			fatal("set mode: %v", err)
		}
		fmt.Printf("memory_mode set to: %s\n", mode)
	default:
		fatal("invalid mode %q (valid: off, dry-run, on)", os.Args[3])
	}
}
