# Contributing

提交问题或修复时，请说明固件版本、CPU 架构、问题状态、脱敏后的复现步骤及测试结果。不要上传连接通道、配置文件、真实抓包、账号信息或包含这些内容的日志。

## 本地检查

```sh
go test -count=1 ./...
go vet ./...
node tests/ui.cjs
python tools/build.py
```

Linux 上另运行 `go test -race -count=1 ./...`，需要 C 编译器。Windows 不执行 Linux 专用测试。

## 隔离安装测试

以下测试创建系统目录、启动进程和模拟 OpenWrt 设施，只能在可丢弃容器中执行。禁止直接在宿主机或真实路由器运行测试脚本。

在 Linux 或已启动 Docker Desktop 的环境，从仓库根目录执行：

```sh
docker run --rm --network none \
  -e RADARSENDER_FIXTURE_CONTAINER=1 \
  -v "$PWD:/src:ro" debian:bookworm-slim \
  sh /src/tests/package-runtime.sh

docker run --rm --network none \
  -e RADARSENDER_FIXTURE_CONTAINER=1 \
  -v "$PWD:/src:ro" debian:bookworm-slim \
  sh /src/tests/install-fixture.sh
```

实际 tcpdump 联测由 `RS_TEST_REAL_TCPDUMP` 启用，测试通过本地 HTTP 接收端确认最终包数；需要 Linux Ethernet 接口与抓包权限。`tests/bundled-tcpdump.sh` 的非原生架构离线执行需要 QEMU/binfmt，原生实时抓包需要 `ping`。完整前提见 [运行说明](docs/operation.md)。

## 必须保持的行为

- 不等待游戏识别、不暂存等待确认的首包，不把识别当作发送开关。
- 离线丢弃、不无限缓存、不写盘、不重放；停止后能得到明确状态。
- 不改变旧 RouterCapture 配置或系统 tcpdump。
- 配置／控制权限保持私有，状态响应不得返回通道凭据。
- 示例仅用合成流量或测试保留地址；新测试不得连接真实雷达端点。

更新 `third_party/` 时，保留原始源码、原始许可和版权通知，更新来源及 SHA-256，重编译并验证相关静态程序。不要只替换二进制而忽略来源和许可。
