package sender

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os/exec"
	"regexp"
	"time"
)

var errLANUnavailable = errors.New("无法自动识别可用 LAN，正在重试；请检查路由器 LAN 设置")
var interfaceName = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,15}$`)

// OpenWrt's active logical LAN is authoritative, including custom bridge/VLAN
// device names. Only a failed ubus command permits the conventional br-lan
// fallback. Never guess from the first Ethernet interface or default route.
func detectLAN(ctx context.Context) (string, error) {
	return resolveLAN(ctx, lanStatus, net.InterfaceByName)
}

func resolveLAN(ctx context.Context, status func(context.Context) ([]byte, error), lookup func(string) (*net.Interface, error)) (string, error) {
	data, err := status(ctx)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	name := "br-lan"
	if err == nil {
		var state struct {
			Up       bool   `json:"up"`
			L3Device string `json:"l3_device"`
			Device   string `json:"device"`
		}
		if json.Unmarshal(data, &state) != nil || !state.Up {
			return "", errLANUnavailable
		}
		name = state.L3Device
		if name == "" {
			name = state.Device
		}
	}
	if !interfaceName.MatchString(name) || name == "lo" || name == "any" || name == "." || name == ".." {
		return "", errLANUnavailable
	}
	iface, err := lookup(name)
	if err != nil || iface == nil || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || len(iface.HardwareAddr) != 6 {
		return "", errLANUnavailable
	}
	return name, nil
}

func lanStatus(parent context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ubus", "call", "network.interface.lan", "status")
	output := &lanOutput{}
	cmd.Stdout = output
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if output.overflow {
		return []byte("invalid"), nil
	}
	return output.data, nil
}

type lanOutput struct {
	data     []byte
	overflow bool
}

func (b *lanOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (64 << 10) - len(b.data)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}
