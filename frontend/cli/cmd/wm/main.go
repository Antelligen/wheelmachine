// Command wm is the command line of the Wheelmachine API: cloud machines for people and agents, paid by the second.
//
// Exit codes: 0 done, 1 the API or the network refused, 2 the command was not right; `wm exec` and `wm sh` exit
// with the command's own code.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	a := &app{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, now: time.Now}
	code := a.run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
