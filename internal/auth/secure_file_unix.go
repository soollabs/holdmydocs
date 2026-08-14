//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package auth

import (
	"os"
	"syscall"
)

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
