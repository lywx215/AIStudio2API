//go:build !windows

package browser

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func configureHeadless(*exec.Cmd)    {}
func bindProcess(cmd *exec.Cmd) (func() error, error) {
	return func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		// Sending SIGKILL alone is not evidence that the complete private
		// process group has exited. Preserve its profile if that cannot be proven.
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			err = syscall.Kill(-cmd.Process.Pid, 0)
			if errors.Is(err, syscall.ESRCH) {
				return nil
			}
			if err != nil {
				return err
			}
			time.Sleep(20 * time.Millisecond)
		}
		return ErrCleanup
	}, nil
}
