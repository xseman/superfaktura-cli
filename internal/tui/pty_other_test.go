//go:build !linux

package tui_test

import (
	"os"
	"syscall"
)

// Everywhere but Linux the frame test skips: opening a pseudo-terminal is the
// one part of it that is not portable, and errUnsupportedPTY is how the test
// tells "this platform cannot run it" from "it ran and was wrong".

func openPTY(_, _ int) (*os.File, string, error) {
	return nil, "", errUnsupportedPTY
}

func sessionAttrs() *syscall.SysProcAttr { return nil }
