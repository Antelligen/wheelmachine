//go:build !(linux || darwin || freebsd || netbsd || dragonfly || windows)

package term

import (
	"errors"
	"os"
)

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func readPassword(*os.File) ([]byte, error) {
	return nil, errors.New("typing cannot be hidden on this system: give the key on standard input instead (wm login < keyfile)")
}

func rawMode(*os.File) (func(), error) {
	return nil, errors.New("this system has no terminal raw mode")
}

func size(*os.File) (int, int, error) {
	return 0, 0, errors.New("this system cannot read the terminal's size")
}
