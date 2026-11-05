//go:build darwin || freebsd || netbsd || dragonfly

package term

import "syscall"

const (
	ioctlGetTermios = syscall.TIOCGETA
	ioctlSetTermios = syscall.TIOCSETA
)
