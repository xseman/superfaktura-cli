//go:build linux

package tui_test

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// A pseudo-terminal, opened with the kernel's own ioctls rather than a
// dependency. Three calls is the whole of it — /dev/ptmx, unlock, ask for the
// number — and the alternative was a module in go.mod for code this size. See
// 000 · Architecture on keeping the dependency list small.

type winsize struct {
	rows, cols, x, y uint16
}

func ioctl(fd, request, arg uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, arg); errno != 0 {
		return errno
	}
	return nil
}

// openPTY returns the master side of a new pseudo-terminal and the path of its
// slave, sized before anything is started so the program under test reads the
// size it is meant to have at startup rather than after a resize.
func openPTY(cols, rows int) (*os.File, string, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, "", err
	}

	var unlock int32
	if err := ioctl(master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != nil {
		_ = master.Close()
		return nil, "", fmt.Errorf("unlocking the pty: %w", err)
	}

	var number uint32
	if err := ioctl(master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&number))); err != nil {
		_ = master.Close()
		return nil, "", fmt.Errorf("naming the pty: %w", err)
	}

	size := winsize{rows: uint16(rows), cols: uint16(cols)}
	if err := ioctl(master.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&size))); err != nil {
		_ = master.Close()
		return nil, "", fmt.Errorf("sizing the pty: %w", err)
	}

	return master, "/dev/pts/" + strconv.Itoa(int(number)), nil
}

// sessionAttrs makes the slave the child's controlling terminal, which is what
// turns isatty on and gives Bubble Tea a size to read.
func sessionAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true}
}
