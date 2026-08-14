//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	current, err := user.Current()
	if err != nil {
		return false
	}
	uid, err := strconv.ParseUint(current.Uid, 10, 32)
	return err == nil && stat.Uid == uint32(uid)
}
