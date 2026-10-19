//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package knowledge

import (
	"os"
	"syscall"
)

func openRegular(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
