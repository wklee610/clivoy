//go:build windows

package daemon

import (
	"os/exec"
	"testing"
)

func checkDetachedProcess(t *testing.T, pid int) {
	t.Helper()
	// Survival after the launcher exits is checked by TestLaunchDetached.
	// Windows has no Unix process-group query; check the creation flags too.
	cmd := &exec.Cmd{}
	if err := configureDetached(cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&0x208 != 0x208 {
		t.Fatalf("missing detached process flags: %+v", cmd.SysProcAttr)
	}
}
