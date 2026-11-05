//go:build linux || darwin || freebsd || netbsd || dragonfly

package term

import (
	"os"
	"os/signal"
	"syscall"
)

// OnResize calls fn once with the terminal's size now, then again on every window change (SIGWINCH), until stop is
// called. fn is never called at once from two goroutines.
func OnResize(f *os.File, fn func(cols, rows int)) (stop func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	done := make(chan struct{})
	report := func() {
		if cols, rows, err := Size(f); err == nil {
			fn(cols, rows)
		}
	}
	report()
	go func() {
		for {
			select {
			case <-sig:
				report()
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(sig)
		close(done)
	}
}
