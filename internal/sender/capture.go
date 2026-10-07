package sender

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"radarsender/internal/radarupload"
	"strings"
	"sync"
	"time"
)

// An upload failure closes only this HTTP stream. The capture session remains
// alive while subsequent connection attempts run.
func runAttempt(parent context.Context, c Config, id string, retry bool, ready func(), report func(Counters), capture *captureSession) (counts Counters, result error) {
	ctx, err := capture.ensure(c.Interface)
	if err != nil {
		return counts, err
	}
	client, err := radarupload.Connect(ctx, c.Channel, radarupload.Options{SenderID: id, AllowExistingInput: retry, OnConnect: capture.flows.track})
	if err != nil {
		if localErr := capture.failure(); localErr != nil {
			return counts, localErr
		}
		return counts, err
	}
	defer client.Close()
	if ctx.Err() != nil {
		if err := capture.failure(); err != nil {
			return counts, err
		}
		return counts, parent.Err()
	}
	queue := newPacketQueue(client.Open(context.Background()))
	capture.attach(queue)
	defer func() {
		capture.detach()
		closeErr := queue.Close()
		counts = queue.Stats()
		if result == nil {
			result = closeErr
		}
	}()
	ready()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return counts, capture.failure()
		case <-queue.done:
			return counts, nil
		case <-ticker.C:
			report(queue.Stats())
		}
	}
}

// Capture is controlled only by the local session, not network availability.
func capturePackets(ctx context.Context, name string, ready func(), packet func([]byte, time.Time)) error {
	iface, err := net.InterfaceByName(name)
	if err != nil || iface.Flags&net.FlagUp == 0 || len(iface.HardwareAddr) != 6 {
		return errors.New("LAN 接口不存在、未启用或不是 Ethernet 接口")
	}
	path, err := findTCPDump("/usr/lib/radarsender/tcpdump")
	if err != nil {
		return err
	}
	cmd := exec.Command(path, "-i", name, "-nn", "-s", "65535", "-B", "2048", "-U", "--immediate-mode", "-w", "-")
	prepareChild(cmd)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var diagnostic limitedLog
	cmd.Stderr = &diagnostic
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errors.New("无法创建 tcpdump 读取管道")
	}
	if err = cmd.Start(); err != nil {
		return errors.New("无法启动 tcpdump，请检查可执行权限和系统依赖")
	}
	finished, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		for {
			select {
			case <-finished:
				return
			case <-ctx.Done():
			}
			interruptChild(cmd.Process)
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case <-finished:
			case <-timer.C:
				_ = cmd.Process.Kill()
				_ = stdout.Close()
			}
			return
		}
	}()
	readErr := readPCAP(stdout, ready, packet)
	interruptChild(cmd.Process)
	_ = stdout.Close()
	timer := time.AfterFunc(2*time.Second, func() { _ = cmd.Process.Kill() })
	waitErr := cmd.Wait()
	timer.Stop()
	close(finished)
	<-watched
	if ctx.Err() != nil {
		return nil
	}
	if text := diagnostic.failure(); text != "" {
		return fmt.Errorf("采集失败：%s", text)
	}
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		return errors.New("tcpdump 异常退出，请查看 radarsender 系统日志")
	}
	return errors.New("tcpdump 意外停止，请检查接口后重新连接")
}

// Prefer the private, statically linked binary shipped with the installer.
// Fall back only when absent, so a damaged installed copy is not hidden.
func findTCPDump(bundled string) (string, error) {
	info, err := os.Stat(bundled)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return "", errors.New("内置 tcpdump 文件或权限异常，请重新安装独立版")
		}
		return bundled, nil
	}
	if !os.IsNotExist(err) {
		return "", errors.New("无法访问内置 tcpdump，请检查安装目录权限")
	}
	path, err := exec.LookPath("tcpdump")
	if err != nil {
		return "", errors.New("缺少 tcpdump，请重新安装独立版或安装支持 --immediate-mode 的系统 tcpdump")
	}
	return path, nil
}

type limitedLog struct {
	mu   sync.Mutex
	data []byte
}

func (l *limitedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	left := 4096 - len(l.data)
	if left > 0 {
		l.data = append(l.data, p[:min(left, n)]...)
	}
	return n, nil
}
func (l *limitedLog) failure() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range strings.Split(string(l.data), "\n") {
		if strings.HasPrefix(line, "tcpdump:") && !strings.Contains(line, "listening on") && !strings.Contains(line, "verbose output") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
