//go:build unix

package termimg

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// cellPixels returns the terminal cell size in pixels if the terminal
// reports it via TIOCGWINSZ.
func cellPixels(fd uintptr) (int, int, bool) {
	ws, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 0, 0, false
	}
	return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row), true
}

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
