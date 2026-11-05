// Package term tells whether a file is a terminal, and reads a line from one without showing what is typed.
package term

import (
	"errors"
	"io"
	"os"
	"os/signal"
)

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool { return f != nil && isTerminal(f) }

// Raw puts f into raw mode, so a remote shell or a full-screen program gets keys as they are typed, and returns a
// function that puts the terminal back as it was. It is an error where f is not a terminal.
func Raw(f *os.File) (restore func(), err error) {
	if f == nil {
		return nil, errors.New("no terminal")
	}
	return rawMode(f)
}

// Size is f's window in columns and rows.
func Size(f *os.File) (cols, rows int, err error) {
	if f == nil {
		return 0, 0, errors.New("no terminal")
	}
	return size(f)
}

// ReadPassword reads one line from the terminal f without showing what is typed; the line's end is not part of it.
func ReadPassword(f *os.File) ([]byte, error) {
	if f == nil {
		return nil, errors.New("no terminal")
	}
	return readPassword(f)
}

// onInterrupt puts the terminal back and ends the program (130, as an interrupted program does) when it is
// interrupted while typing is hidden: a terminal left showing nothing of what is typed is worse than an exit.
func onInterrupt(restore func()) (stop func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			restore()
			_, _ = os.Stderr.WriteString("\n")
			os.Exit(130)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sig)
		close(done)
	}
}

// readLine reads up to a line's end, a byte at a time, so nothing after it is taken from the terminal.
func readLine(r io.Reader) ([]byte, error) {
	var b [1]byte
	var line []byte
	for {
		n, err := r.Read(b[:])
		if n > 0 {
			switch b[0] {
			case '\n':
				return line, nil
			case '\r':
			default:
				line = append(line, b[0])
			}
			if len(line) > 8192 {
				return nil, errors.New("the line is longer than any key")
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return line, nil
			}
			return nil, err
		}
	}
}
