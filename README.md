# RadarSender

独立的 OpenWrt / iStoreOS 雷达发射端，当前版本 **0.1.4**。

自动识别 LAN 接口，将 Ethernet PCAP 实时上传到兼容的雷达接收端。LuCI 页面只需填写连接通道，点击连接／断开。不包含雷达服务端、游戏协议解码、Windows 客户端或旧版 RouterCapture。

## 功能与边界

- **延迟优先**：不等待游戏识别、不凑批、不做游戏协议或端口筛选，首包立即进入发送队列。
- **自动 LAN**：读取 OpenWrt LAN 配置，支持自定义桥和 VLAN；排除自身上传连接，避免循环采集。
- **独立运行**：单独的 `radarsender` 服务、配置和 LuCI 菜单，不修改旧 RouterCapture。
- **离线重连**：持续采集，离线数据立即丢弃并计数，不写磁盘、不重放；固定等待 2 秒后重试。
- **有界队列**：最多 512 包／4 MiB，超出容量丢弃副本并计数，不阻塞采集。
- **四种架构**：ARM、ARM64、x86、x64；内置独立的静态 tcpdump，无需在线下载安装依赖。
- 开机只启动管理服务，**不会自动开始发送**；状态接口不返回连接通道凭据。

采集覆盖所选 LAN 接口的流量，包括非游戏流量。请仅连接到你信任并管理的接收端，优先使用 HTTPS 通道；明文 HTTP 不提供传输加密。不要公开连接通道、配置文件或真实抓包。实际吞吐和延迟受硬件、链路和接收端影响，不能保证零延迟、零丢包或所有固件兼容。

## 安装

从 [Releases](https://github.com/dmahck/radarsender/releases) 下载 `luci-app-radarsender_0.1.4_universal.run`，上传到路由器 `/tmp`。也可用下文命令自行构建。

```sh
sh /tmp/luci-app-radarsender_0.1.4_universal.run --verify
sh /tmp/luci-app-radarsender_0.1.4_universal.run --check
sh /tmp/luci-app-radarsender_0.1.4_universal.run
```

安装前提：现有 **LuCI、rpcd、procd、jsonfilter、ubus**，Linux 内核支持包捕获，具备抓包权限。MIPS 不在支持范围。安装器不联网下载依赖；这是独立 portable 安装包，不登记为 opkg/apk 包。

安装后刷新 LuCI，进入 **服务 → 雷达发射（独立版）**，粘贴兼容服务器生成的连接通道。旧发送端若使用同一通道，先停止旧发送端。

如需完整观察经过路由器的流量，请自行检查软件／硬件流量加速；本程序不修改防火墙或厂商加速设置。旁路和交换机内部转发的流量不一定经过采集接口。

## 从源码构建

需要 Go 1.23+ 和 Python 3.10+；Node.js 供 UI 测试使用。无第三方 Go 模块。仓库包含打包所需的固定版本第三方源码、许可证和预编译 tcpdump。

```sh
git clone https://github.com/dmahck/radarsender.git
cd radarsender
go test -count=1 ./...
go vet ./...
node tests/ui.cjs
python tools/build.py
```

输出位于 `dist/0.1.4/`，包括通用 `.run`／`.tar.gz`、各架构包、`manifest.json` 和 `SHA256SUMS`。构建验证 tcpdump 的 SHA-256、补丁清单及所有输出的静态 ELF 架构，不导入旧工程。

重编译第三方 tcpdump 需要 Docker 和构建环境网络：

```sh
python tools/build_tcpdump.py
python tools/build.py
```

固定源版本、编译器和适配补丁见 [third_party/tcpdump/README.md](third_party/tcpdump/README.md)。

## 管理和卸载

```sh
/usr/sbin/radarsender version
ubus call radarsender status '{}'
ubus call radarsender stop '{}'
logread -e radarsender
/etc/init.d/radarsender restart
sh /usr/lib/radarsender/install.sh --uninstall
```

重启服务会断开发送。卸载保留 `/etc/radarsender/config.json`，其中含连接通道，请勿上传或公开。升级失败会尝试回滚独立版文件，不替换系统 tcpdump。

## 项目结构

| 目录 | 内容 |
| --- | --- |
| `cmd/radarsender/` | Linux 管理服务与 RPC 命令入口 |
| `internal/sender/` | LAN 识别、采集、队列、会话与状态管理 |
| `internal/radarupload/` | 通道认证与 HTTP PCAP 上传 |
| `openwrt/root/` | procd 服务、LuCI 页面、rpcd ACL |
| `tools/` | 安装器、四架构打包、tcpdump 构建 |
| `tests/` | UI、合成 PCAP、隔离运行和安装夹具 |
| `third_party/tcpdump/` | 固定来源、源码、静态程序及原始许可 |
| `docs/` | 完整使用说明和贡献说明 |

## 文档、测试与许可

- [完整运行说明与限制](docs/operation.md)
- [贡献与隔离测试](CONTRIBUTING.md)
- [安全问题报告](SECURITY.md)
- [0.1.4 更新说明](CHANGELOG.md)
- [第三方来源与许可](THIRD_PARTY_NOTICES.md)

Windows 常规 Go 测试不会执行 Linux 专用采集／进程测试。CI 在 Linux 运行单元、并发检查、UI 和四架构打包，并在一次性容器验证安装／回滚／卸载。交叉编译和容器测试不等于所有目标路由器实机验收；ARM／ARM64 实时抓包仍需对应原生硬件验证。

原创代码采用 [MIT License](LICENSE)。`third_party/` 中的代码、归档、二进制及许可保持各自的原始许可证，不因本项目 MIT 声明而重新许可。
