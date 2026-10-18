//go:build windows

package credentialops

import "os/exec"

func configureRuntimeProcess(cmd *exec.Cmd) {}
func stopRuntimeProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
