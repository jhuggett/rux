//go:build !unix

package termimg

import "os/exec"

func cellPixels(fd uintptr) (int, int, bool) { return 0, 0, false }

func detach(cmd *exec.Cmd) {}
