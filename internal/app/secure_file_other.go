//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package app

import "os"

func ownedByCurrentUser(info os.FileInfo) bool {
	return true
}
