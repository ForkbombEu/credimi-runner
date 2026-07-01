//go:build windows

package runtime

import "os/exec"

func detachCommand(*exec.Cmd) {}
