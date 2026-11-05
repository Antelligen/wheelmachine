// Command wm-mcp is the Wheelmachine API as an MCP server over standard input and output: JSON-RPC 2.0, a message
// a line. It logs to standard error only; standard output carries nothing but messages.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"wheelmachine/frontend/cli/api"
)

const help = `wm-mcp - the Wheelmachine API as an MCP server, over standard input and output

An MCP client starts it and speaks JSON-RPC 2.0 with it, one message a line. It speaks MCP
2026-07-28, where every request carries its version, and 2025-11-25, 2025-06-18, 2025-03-26 and
2024-11-05, which begin with initialize. It writes its log to standard error.

The API key comes from WHEELMACHINE_API_KEY, else from the one wm login kept; the API is at
WHEELMACHINE_BASE_URL, else https://api.wheelmachine.ai.

Tools: list_machines, create_machine, get_machine, run_command, read_file, write_file,
pause_machine, start_machine, stop_machine, fork_machine, backup_machine, list_backups,
delete_machine, list_gpus, list_regions, list_secrets, give_secrets, set_network, list_apps,
turn_app_on, turn_app_off, show_app, open_app, whoami.
`

func main() {
	for _, arg := range os.Args[1:] {
		switch strings.TrimLeft(arg, "-") {
		case "version", "v":
			fmt.Println("wm-mcp " + api.Version)
			return
		case "help", "h":
			fmt.Print(help)
			return
		}
		fmt.Fprintf(os.Stderr, "wm-mcp: it takes no %q; see wm-mcp -h\n", arg)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.New(os.Stderr, "wm-mcp: ", log.LstdFlags)
	s := newServer(logger, func() (*api.Client, error) {
		return api.NewFromEnv(os.Getenv, api.WithUserAgent("wm-mcp/"+api.Version))
	})
	logger.Printf("%s ready: MCP %s, and %s after initialize", api.Version, latestVersion, strings.Join(handshakeVersions, ", "))
	if err := s.serve(ctx, os.Stdin, os.Stdout); err != nil {
		logger.Printf("stopped: %v", err)
		os.Exit(1)
	}
}
