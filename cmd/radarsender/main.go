//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"radarsender/internal/sender"
	"syscall"
	"time"
)

const runtimeDir = "/var/run/radarsender"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 1 && args[0] == "version" {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"version": sender.Version})
	}
	if len(args) == 1 && args[0] == "list" {
		return json.NewEncoder(os.Stdout).Encode(sender.Methods)
	}
	if len(args) == 2 && args[0] == "call" {
		return call(args[1])
	}
	if len(args) > 0 && args[0] == "serve" {
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		config := flags.String("config-dir", "/etc/radarsender", "私有配置目录")
		state := flags.String("runtime-dir", runtimeDir, "私有运行目录")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("无效参数")
		}
		return serve(*config, *state)
	}
	return errors.New("usage: radarsender serve | list | call METHOD | version")
}
func call(method string) error {
	output := func(msg string) error { return json.NewEncoder(os.Stdout).Encode(sender.Failure(msg)) }
	if _, ok := sender.Methods[method]; !ok {
		return output("不支持的操作")
	}
	params, err := io.ReadAll(io.LimitReader(os.Stdin, 8193))
	if err != nil || len(params) > 8192 {
		return output("请求过大")
	}
	if len(bytes.TrimSpace(params)) == 0 {
		params = []byte("{}")
	}
	if !json.Valid(params) {
		return output("请求必须为 JSON")
	}
	b, _ := json.Marshal(sender.Request{Method: method, Params: params})
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", filepath.Join(runtimeDir, "control.sock"))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Post("http://radarsender/rpc", "application/json", bytes.NewReader(b))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return output("雷达发射后台响应超过 5 秒，请查看 radarsender 日志")
		}
		return output("雷达发射后台未启动或控制接口不可用，请查看 radarsender 服务日志")
	}
	defer response.Body.Close()
	_, err = io.Copy(os.Stdout, io.LimitReader(response.Body, 65536))
	return err
}
func serve(configDir, stateDir string) error {
	if os.Geteuid() != 0 {
		return errors.New("雷达发射服务需要 root 权限")
	}
	for _, dir := range []string{configDir, stateDir} {
		if !filepath.IsAbs(dir) {
			return errors.New("服务目录必须为绝对路径")
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return errors.New("无法创建服务目录")
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("服务目录不是普通目录")
		}
		if err = os.Chmod(dir, 0700); err != nil {
			return err
		}
	}
	lock, err := os.OpenFile(filepath.Join(stateDir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("雷达发射后台已经运行")
	}
	path := filepath.Join(stateDir, "control.sock")
	if info, e := os.Lstat(path); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("控制接口路径被普通文件占用")
		}
		if e = os.Remove(path); e != nil {
			return e
		}
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(path)
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	service := sender.New(configDir)
	if warning := service.Snapshot().ConfigError; warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	server := &http.Server{Handler: service.Handler(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
	ended := make(chan error, 1)
	go func() { ended <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
	case err = <-ended:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	closeErr := service.Close(shutdown)
	_ = server.Close()
	if closeErr != nil {
		return errors.New("发送任务停止超时")
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
