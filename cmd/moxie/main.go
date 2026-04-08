package main

import (
	"fmt"
	"os"
)

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// --- CLI entrypoint ---

var commandHandlers = map[string]func(){
	"init":     cmdInit,
	"send":     cmdSend,
	"messages": cmdMessages,
	"msg":      cmdMessages,
	"poll":     cmdPoll,
	"cursor":   cmdCursor,
	"schedule": cmdSchedule,
	"subagent": cmdSubagent,
	"workflow": cmdWorkflow,
	"result":   cmdResult,
	"threads":  cmdThreads,
	"voice":    cmdVoice,
	"memory":   cmdMemory,
	"service":  cmdService,
	"serve":    cmdServe,
	"help":     usage,
	"--help":   usage,
	"-h":       usage,
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	if handler, ok := commandHandlers[os.Args[1]]; ok {
		handler()
		return
	}

	fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
	usage()
	os.Exit(1)
}

func usage() {
	fmt.Println(`moxie — Chat agent CLI

Usage:
  moxie init                              Configure bot token and chat ID
  moxie send <message>                    Send a message
  moxie messages [--json|--raw] [-n N]    List recent messages (default: markdown)
  moxie msg                               Alias for messages
  moxie schedule <subcommand>             Manage schedules
  moxie subagent <subcommand>             Manage and delegate subagent work
  moxie workflow <subcommand>             Run supervised orchestration workflows
  moxie result <subcommand>               Retrieve subagent results
  moxie threads show <id>                 Show turns for a thread
  moxie voice <path|show|reset>           Manage Moxie's adjustable style memory
  moxie memory <subcommand>              Query and inspect stored memories
  moxie service <subcommand>              Control the background service
  moxie serve [--cwd <dir>] [--transport <telegram|slack|webex>]  Run configured chat transports and dispatch to agent backends`)
}
