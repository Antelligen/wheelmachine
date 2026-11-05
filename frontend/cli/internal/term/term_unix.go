//go:build linux || darwin || freebsd || netbsd || dragonfly

package term

import (
	"os"
	"syscall"
	"unsafe"
)

func getState(fd uintptr) (*syscall.Termios, error) {
	var t syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlGetTermios, uintptr(unsafe.Pointer(&t))); e != 0 {
		return nil, e
	}
	return &t, nil
}

func setState(fd uintptr, t *syscall.Termios) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlSetTermios, uintptr(unsafe.Pointer(t))); e != 0 {
		return e
	}
	return nil
}

// withFd runs fn with f's descriptor, leaving f as it is (Fd would put it into blocking mode).
func withFd(f *os.File, fn func(fd uintptr)) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	return rc.Control(fn)
}

func isTerminal(f *os.File) bool {
	ok := false
	if err := withFd(f, func(fd uintptr) {
		_, err := getState(fd)
		ok = err == nil
	}); err != nil {
		return false
	}
	return ok
}

// makeRaw puts the terminal into raw mode - no echo, no line editing, no signals from keys, bytes delivered as they
// are typed - and returns how it was, to put it back with. What a full-screen program or a remote shell needs.
func makeRaw(f *os.File) (*syscall.Termios, error) {
	var old *syscall.Termios
	var err error
	cerr := withFd(f, func(fd uintptr) {
		if old, err = getState(fd); err != nil {
			return
		}
		t := *old
		t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
		t.Oflag &^= syscall.OPOST
		t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
		t.Cflag &^= syscall.CSIZE | syscall.PARENB
		t.Cflag |= syscall.CS8
		t.Cc[syscall.VMIN] = 1
		t.Cc[syscall.VTIME] = 0
		err = setState(fd, &t)
	})
	if cerr != nil {
		return nil, cerr
	}
	if err != nil {
		return nil, err
	}
	return old, nil
}

func restore(f *os.File, old *syscall.Termios) error {
	return withFd(f, func(fd uintptr) { _ = setState(fd, old) })
}

// winsize is the terminal's window, as the TIOCGWINSZ ioctl gives it.
type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

func getSize(f *os.File) (cols, rows int, err error) {
	var ws winsize
	cerr := withFd(f, func(fd uintptr) {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); e != 0 {
			err = e
		}
	})
	if cerr != nil {
		return 0, 0, cerr
	}
	if err != nil {
		return 0, 0, err
	}
	return int(ws.Col), int(ws.Row), nil
}

func readPassword(f *os.File) ([]byte, error) {
	var old *syscall.Termios
	var err error
	if cerr := withFd(f, func(fd uintptr) {
		if old, err = getState(fd); err != nil {
			return
		}
		t := *old
		t.Lflag &^= syscall.ECHO
		t.Lflag |= syscall.ICANON | syscall.ISIG
		t.Iflag |= syscall.ICRNL
		err = setState(fd, &t)
	}); cerr != nil {
		return nil, cerr
	}
	if err != nil {
		return nil, err
	}
	restore := func() { _ = withFd(f, func(fd uintptr) { _ = setState(fd, old) }) }
	defer restore()
	stop := onInterrupt(restore)
	defer stop()
	return readLine(f)
}

// rawMode and size are the unix forms Raw and Size call.
func rawMode(f *os.File) (func(), error) {
	old, err := makeRaw(f)
	if err != nil {
		return nil, err
	}
	stop := onInterrupt(func() { _ = restore(f, old) })
	return func() { stop(); _ = restore(f, old) }, nil
}

func size(f *os.File) (int, int, error) { return getSize(f) }
