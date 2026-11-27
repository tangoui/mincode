package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stdout, agentControlUsage)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runAgentControl(ctx, args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mincode:", err)
		os.Exit(commandExitCode(err))
	}
}
