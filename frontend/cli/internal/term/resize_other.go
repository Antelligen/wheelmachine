//go:build !(linux || darwin || freebsd || netbsd || dragonfly)

package term

import "os"

// OnResize reports the size once; where there is no window-change signal (Windows, others) it does not watch for more.
func OnResize(f *os.File, fn func(cols, rows int)) (stop func()) {
	if cols, rows, err := Size(f); err == nil {
		fn(cols, rows)
	}
	return func() {}
}
