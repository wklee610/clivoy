//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package knowledge

import "os"

func openRegular(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
