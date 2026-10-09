# RadarSender 0.1.6

独立软路由雷达发射端。只采集并发送 Ethernet PCAP，不依赖旧版 RouterCapture 配置、SMB、存盘、设备识别或握手组件。

原项目及交付包保持原样。独立程序使用 radarsender 服务、/etc/radarsender 配置和 /var/run/radarsender/control.sock。

## 0.1.6 更新

新增第五架构 `mipsel`，面向 MT7621，使用 MIPS32r2 little-endian／软件浮点基线。安装器核对实际用户态 ELF，拒绝 big-endian MIPS、MIPS64 和错误架构；随后以随包二进制自检确认能否运行。交叉编译或 QEMU 运行不等于目标设备吞吐、抓包覆盖和低丢包验收。保留原有实时发送策略。

## 0.1.5 更新（历史）

逐包过滤、内存复用和管道读优化，新增小型单架构安装器。完整说明见 [性能优化与丢包边界](performance.md)。运行策略、通道和队列上限保持不变。

## 0.1.4 更新（历史）

- 明确延迟优先策略：宁愿多发，不等待游戏识别，不采用确认后才放行的筛选方案。
- 增加首个未知协议包立即入队、在第二包或上传结束前到达 HTTP 接收端的回归测试。
- 同步程序、安装标记、构建包及验收脚本版本。发送逻辑与 0.1.3 保持一致，未加入游戏分类器；保留内置 tcpdump、自动 LAN、全速发送和离线采集行为。

## 与原版的区别

- 只保留通道认证、Ethernet PCAP 发送、网络重连、停止确认和状态查询。
- 独立 LuCI 菜单「雷达发射（独立版）」，只有一个通道输入框和一个连接／断开按钮，连接时自动保存。
- 不需要 SMB/CIFS、共享账号、抓包保存、游戏识别、UDP 镜像或握手/密钥组件。
- 不修改旧版 routercapture 文件或配置，也不自动启动旧版或复用其通道。
- 配置损坏不会阻止管理后台启动：页面显示具体配置错误，重新输入通道并点击连接即可修复。
- 每次开机只启动管理服务，不自动发送；通道保存在 root 私有文件中，状态响应不返回凭据。

## 安装

推荐 `dist/0.1.6/luci-app-radarsender_0.1.6_universal.run`，支持 ARM、ARM64、x86、x64、MIPS32 little-endian（`mipsel`）。上传到 iStore 的手动安装，或在路由器执行：

```sh
sh /tmp/luci-app-radarsender_0.1.6_universal.run --verify
sh /tmp/luci-app-radarsender_0.1.6_universal.run --check
sh /tmp/luci-app-radarsender_0.1.6_universal.run
```

需要现有 LuCI、rpcd、procd、jsonfilter 和 ubus。已内置静态 tcpdump 4.99.7 与 libpcap 1.11.0，不需要通过 opkg/apk 安装 tcpdump 或 libpcap，也不需要共享库、CIFS 或 firewall4。安装器不会联网下载依赖。MIPS 仅支持 ELF32 little-endian 用户态，面向 MT7621；不支持 big-endian MIPS 或 MIPS64，架构识别还需要 `od`。

空间紧张的 MT7621 设备可选择 `luci-app-radarsender_0.1.6_mipsel.run`，其他设备可选择对应 `_arm.run`、`_arm64.run`、`_x86.run` 或 `_x64.run`；单架构包与通用包采用同样的校验、预检和安装流程，缺少本机架构时在修改文件前拒绝安装。

这是独立 portable 安装包，不注册为 opkg/apk 包。安装前校验全部文件；升级自己的版本时需要先断开发送，保留配置，安装失败会尝试恢复旧文件。旧 RouterCapture 可以保留用于本地保存，但同一通道不能同时运行两个发送端。

安装后刷新 LuCI，进入「服务 → 雷达发射（独立版）」。原页面仍对应旧服务。旧服务如果正在发送，先在旧页面断开，再连接独立版。

## 内置 tcpdump

安装器按 CPU 选择随包的静态程序，安装到 `/usr/lib/radarsender/tcpdump`，安装前验证可执行性与 `--immediate-mode` 支持。发送端优先使用这个私有程序；仅当文件不存在时才回退到系统 tcpdump。内置文件损坏或权限错误会明确报错，不会静默切换。

不替换、不卸载系统已有 tcpdump。升级失败会恢复私有程序，卸载只删除独立版文件并保留通道配置。许可证和来源记录安装在 `/usr/lib/radarsender/licenses/`。

五种架构的私有 tcpdump 均静态链接 musl/libpcap，Go 发送端禁用 CGO；不依赖固件的动态加载器和 libc 版本。ARM 使用 ARMv5TE 软件浮点基线，ARM64 使用 ARMv8-A 基线，x86 使用 Pentium 4 基线，x64 使用 x86-64 基线；MIPS tcpdump 使用 MIPS32r2 little-endian／软件浮点基线，Go 发送端使用 `GOARCH=mipsle GOMIPS=softfloat`。实际运行仍需要固件内核支持包捕获，并具备抓包权限；静态编译不能保证所有固件实机都兼容。

`uname -m` 在 MT7621 上可能显示 `mips`，不能据此判断端序。安装器读取 `/bin/busybox` 或 `/bin/sh` 实际 ELF 的位数、端序与 machine 字段，脚本或不存在的候选才回退到下一个；已有但不兼容的 ELF 不会被另一个候选掩盖。ELF 识别后仍需二进制运行预检，不能仅靠通用 MIPS 标签判断 ISA 兼容。

## 自动 LAN 与全速发送

每次连接和重连从 OpenWrt `ubus call network.interface.lan status` 读取实际 LAN 设备，优先 `l3_device`，兼容 `device`。支持自定义桥名和 VLAN；验证接口已启用且为 Ethernet。仅 ubus 调用失败时尝试有效的 `br-lan`；LAN 明确关闭、返回无效设备或状态损坏时不猜选其他网口。无法识别时显示原因，每次失败后固定等待 2 秒重试，可随时断开。页面只读显示识别结果。

发送整个识别出的 LAN 接口上的 Ethernet 帧，自动排除本次发送端自身 HTTP/TCP 连接。没有设备选择或游戏协议过滤。已删除应用限速器，始终尽力全速发送；实际吞吐受 CPU、网卡、上行网络与接收端限制。升级保留旧通道，旧 `interface` 和 `max_rate_kbps` 字段不再参与运行，下一次连接保存时移除。

### MT7621 捕获范围建议

低功耗设备不宜无差别镜像大量下载、视频或其他无关流量。优先将目标设备放在独立 LAN／VLAN，使自动识别的 LAN 限于所需流量，同时确认目标包确实经过这个接口。当前页面没有自定义接口、设备、端口或 BPF 过滤选项；若另外配置捕获入口或开发过滤，应事先明确目标接口／流量并验证覆盖，不等待游戏分类确认、不暂存首包，也不要把减少输入流量误称为发送端性能提升。

MIPS 新增架构支持不代表 MT7621 能承载任意捕获速率。实机对比应保持设备、固件和流量相同，记录 CPU、进程 RSS、捕获与应用丢包计数、重连次数和接收端最终包数；页面应用丢包为零不能证明网卡／内核没有漏采。

### 延迟优先：宁愿多发，不等待识别

在线时，第一包及未识别流量同样立即入队并唤醒发送，不等待游戏确认、不暂存首包、不等凑批。tcpdump 使用 `-U --immediate-mode`，上传逐条写出 PCAP record。仅保留原有的自身上传连接排除，防止重复采集自身发送流量。

历史 CF 抓包分析中的“确认后放行＋前缀暂存”是离线评估方案，未接入本版；该方案会引入约 2–5 秒首次确认等待，因此不采用。以后若增加游戏识别，只能在不阻塞转发的旁路中执行，不能以等待识别结果作为发送条件；当前版本尚无旁路游戏分类器。

这表示不额外引入识别等待，不是绝对零延迟保证。通道认证、服务端启动、网络传输和拥塞仍可能产生等待；较多非游戏流量也可能占用上行带宽。首次雷达显示速度还取决于服务端何时收到并解析到有效游戏数据。以下队列上限和离线丢弃策略保持不变。

上传队列最多 512 包、4 MiB；tcpdump 内核缓冲请求为 2048 KiB。超过队列容量时丢弃副本并计数，不通过阻塞采集来排队。断开时未发送的排队包计入丢包；已经写出的包会核对服务器最终确认数量。显示的丢包数是应用队列和离线丢包，不含网卡、内核和旁路漏采，不能视为零丢包证明。

本版不自动更改防火墙或流量加速设置。要完整观察经过路由器的流量，应在路由器界面关闭相关软件/硬件流量加速；厂商 NSS/SFE/PPE 也需单独确认。旁路、交换机内部转发以及不经过所选接口的流量无法保证采集。

服务器离线不会停止本地抓包进程。点击连接后先启动采集，再建立 HTTP 上传；连接失败后固定等待 2 秒重试（另加本次连接或网络超时），直到恢复或手动断开。服务器恢复后使用同一采集进程继续上传，不重放旧包。LAN 设备确实改变时才替换采集进程。认证失效、输入冲突、格式错误和本地采集故障仍会停止并显示原因。重连沿用本次会话的 sender ID。

离线期间立即丢弃数据并累计采集数、离线丢包数，不写磁盘、不无限缓存。界面显示“持续采集中，等待服务器”，不把离线采集算作发送成功。此版本仍使用现有 HTTP/TCP 协议；服务器无连接时不能实际送达数据。若需要无视连接状态的 UDP 盲发，必须配套修改接收协议。

## 管理和卸载

```sh
/usr/sbin/radarsender version
ubus call radarsender status '{}'
ubus call radarsender stop '{}'
logread -e radarsender
/etc/init.d/radarsender restart
sh /usr/lib/radarsender/install.sh --print-target
sh /usr/lib/radarsender/install.sh --uninstall
```

服务重启会断开发送。卸载保留 `/etc/radarsender/config.json`，其中含连接通道，不要公开文件内容。配置与控制目录权限为 700，配置及控制 socket 权限为 600。普通状态权限不允许修改设置、启动或停止。

`--print-target` 只读显示安装架构，不要求 root，不创建安装目录或启动／停止服务。它与安装使用同一架构判断，但不会替代 `.run --check` 对依赖、校验和程序可运行性的完整预检。

## 构建和测试

```sh
go test ./...
go vet ./...
node tests/ui.cjs
python tools/build.py
python -m unittest discover -s tests -p test_arch_selection.py -v
```

Go 1.23+、Python 3、Node.js；无第三方 Go 模块。源码交付包含预编译私有 tcpdump、固定版本的第三方源码和许可证，常规 `python tools/build.py` 可直接打包；若需重编译 tcpdump，执行 `python tools/build_tcpdump.py`，需要 Docker 和构建环境网络。来源、SHA-256 及编译方法见 `third_party/tcpdump/README.md`。Linux 上另有采集进程/HTTP 上传/停止确认联测及实际 tcpdump 离线 PCAP 测试；并发检测使用 `go test -race ./...`，需要 C 编译器。Windows 的常规 `go test` 不运行 Linux 专用测试。

测试及构建记录存放在 `artifacts/`。架构选择测试使用真实 POSIX shell／`od` 读取合成 ELF，可在 Linux 或 Windows Git shell 中运行，不操作真实路由器目录。本地或容器测试不等于目标路由器上的部署验收，ARM／ARM64／x86／MIPS 交叉编译也不代表对应实机已运行。

`tests/package-runtime.sh` 验证发布包及实际后台进程；`tests/install-fixture.sh` 使用模拟 OpenWrt 服务设施验证安装/回滚/卸载。这两个脚本只能在一次性 Docker 容器中运行，必须设置 `RADARSENDER_FIXTURE_CONTAINER=1`，并将本项目只读挂载到 `/src`，不要在真实路由器上运行测试脚本。

`tests/bundled-tcpdump.sh` 在一次性容器中检查五种架构的版本、参数和 PCAP 读写；真实回环抓包默认验证本机 Linux 可执行的 x64／x86，可用 `RADARSENDER_LIVE_TARGETS` 指定原生平台。ARM／ARM64／MIPS 可通过 QEMU 检查程序启动与离线读写；QEMU 缺少部分 PACKET 套接字功能，实时抓包需对应原生硬件验收，QEMU 结果不能换算成 MT7621 吞吐或丢包率。Linux 联测设置 `RS_TEST_REAL_TCPDUMP` 为容器内实际发布二进制路径后，会额外发送一个合成 Ethernet 帧，验证真实 tcpdump → 发送端 → 本地 HTTP 接收端及最终包数确认。

`tests/preview.html` 是使用实际页面代码的本地布局/交互验证页，RPC 状态由测试夹具模拟，不连接路由器或雷达服务器。
