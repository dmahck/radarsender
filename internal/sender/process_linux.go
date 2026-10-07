package sender

import (
	"os"
	"os/exec"
	"syscall"
)

func prepareChild(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL} }
func interruptChild(p *os.Process) {
	if p != nil {
		_ = p.Signal(syscall.SIGINT)
	}
}
