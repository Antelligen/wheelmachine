//go:build windows

package term

import (
	"os"
	"syscall"
	"unsafe"
)

var setConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")

const (
	enableProcessedInput = 0x0001
	enableLineInput      = 0x0002
	enableEchoInput      = 0x0004
)

func isTerminal(f *os.File) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode) == nil
}

func readPassword(f *os.File) ([]byte, error) {
	h := syscall.Handle(f.Fd())
	var old uint32
	if err := syscall.GetConsoleMode(h, &old); err != nil {
		return nil, err
	}
	mode := old&^enableEchoInput | enableProcessedInput | enableLineInput
	if ok, _, err := setConsoleMode.Call(uintptr(h), uintptr(mode)); ok == 0 {
		return nil, err
	}
	restore := func() { _, _, _ = setConsoleMode.Call(uintptr(h), uintptr(old)) }
	defer restore()
	stop := onInterrupt(restore)
	defer stop()
	return readLine(f)
}

// Windows has no SIGWINCH and no POSIX termios; a terminal there is driven in raw mode by the console API.
const (
	enableVirtualTerminalInput      = 0x0200
	enableVirtualTerminalProcessing = 0x0004
)

var getConsoleScreenBufferInfo = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleScreenBufferInfo")

func rawMode(f *os.File) (func(), error) {
	h := syscall.Handle(f.Fd())
	var old uint32
	if err := syscall.GetConsoleMode(h, &old); err != nil {
		return nil, err
	}
	mode := old &^ (enableEchoInput | enableLineInput | enableProcessedInput)
	mode |= enableVirtualTerminalInput
	if ok, _, err := setConsoleMode.Call(uintptr(h), uintptr(mode)); ok == 0 {
		return nil, err
	}
	restore := func() { _, _, _ = setConsoleMode.Call(uintptr(h), uintptr(old)) }
	stop := onInterrupt(restore)
	return func() { stop(); restore() }, nil
}

func size(f *os.File) (int, int, error) {
	var info struct {
		size          struct{ x, y int16 }
		cursor        struct{ x, y int16 }
		attrs         uint16
		left, top     int16
		right, bottom int16
		maxSize       struct{ x, y int16 }
	}
	if ok, _, err := getConsoleScreenBufferInfo.Call(f.Fd(), uintptr(unsafe.Pointer(&info))); ok == 0 {
		return 0, 0, err
	}
	return int(info.right - info.left + 1), int(info.bottom - info.top + 1), nil
}
