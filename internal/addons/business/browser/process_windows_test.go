//go:build windows

package browser

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLauncherConfiguresIndependentSuspendedProcess(t *testing.T) {
	command := exec.Command("not-executed.exe")
	configureProcess(command)
	want := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED)
	if command.SysProcAttr == nil || command.SysProcAttr.CreationFlags&want != want {
		t.Fatal("browser may run before assignment to its own process job")
	}
}
