package sender

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestAutomaticLANSelection(t *testing.T) {
	for _, tc := range []struct {
		name, response, want       string
		commandFailed, unavailable bool
	}{
		{name: "active custom bridge", response: `{"up":true,"l3_device":"br-home","device":"eth0"}`, want: "br-home"},
		{name: "VLAN", response: `{"up":true,"l3_device":"br-lan.10"}`, want: "br-lan.10"},
		{name: "legacy device", response: `{"up":true,"device":"lan0"}`, want: "lan0"},
		{name: "ubus unavailable fallback", commandFailed: true, want: "br-lan"},
		{name: "down LAN never falls back", response: `{"up":false,"l3_device":"br-lan"}`},
		{name: "missing device", response: `{"up":true}`},
		{name: "malformed status never falls back", response: `not-json`},
		{name: "no loopback", response: `{"up":true,"l3_device":"lo"}`},
		{name: "no all-interface capture", response: `{"up":true,"l3_device":"any"}`},
		{name: "unavailable fallback never guesses WAN", commandFailed: true, unavailable: true},
		{name: "device disappeared", response: `{"up":true,"l3_device":"br-home"}`, unavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requested := ""
			get := func(context.Context) ([]byte, error) {
				if tc.commandFailed {
					return nil, errors.New("unavailable")
				}
				return []byte(tc.response), nil
			}
			lookup := func(name string) (*net.Interface, error) {
				requested = name
				if tc.unavailable {
					return nil, errors.New("not found")
				}
				return &net.Interface{Name: name, Flags: net.FlagUp, HardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, 1}}, nil
			}
			got, err := resolveLAN(context.Background(), get, lookup)
			if got != tc.want || (err == nil) != (tc.want != "") {
				t.Fatalf("got %q err=%v", got, err)
			}
			if tc.commandFailed && requested != "br-lan" {
				t.Fatal("fallback guessed an unrelated interface")
			}
			if !tc.commandFailed && tc.want == "" && !tc.unavailable && requested != "" {
				t.Fatal("invalid authoritative status used fallback")
			}
		})
	}
}

func TestLANMustBeUpEthernet(t *testing.T) {
	for _, iface := range []*net.Interface{nil, {Flags: net.FlagUp | net.FlagLoopback, HardwareAddr: make([]byte, 6)}, {Flags: net.FlagUp}, {HardwareAddr: make([]byte, 6)}} {
		get := func(context.Context) ([]byte, error) { return []byte(`{"up":true,"device":"lan0"}`), nil }
		if _, err := resolveLAN(context.Background(), get, func(string) (*net.Interface, error) { return iface, nil }); !errors.Is(err, errLANUnavailable) {
			t.Fatal("non-Ethernet or down interface selected")
		}
	}
}

func TestLANDetectionCancellationAndOutputBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := resolveLAN(ctx, func(context.Context) ([]byte, error) { return nil, errors.New("failed") }, func(string) (*net.Interface, error) { t.Fatal("lookup after cancellation"); return nil, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	b := &lanOutput{}
	input := []byte(strings.Repeat("x", 100000))
	n, err := b.Write(input)
	if err != nil || n != len(input) || len(b.data) != 64<<10 || !b.overflow {
		t.Fatal("LAN output not bounded")
	}
}
