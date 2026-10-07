//go:build !linux

package sender

import (
	"os"
	"os/exec"
)

func prepareChild(cmd *exec.Cmd) {}
func interruptChild(p *os.Process) {
	if p != nil {
		_ = p.Kill()
	}
}
