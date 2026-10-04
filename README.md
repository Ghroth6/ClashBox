# ClashBox 个人派生分支

本仓保留 ClashBox 应用上游代码，并维护配置原文与资源保留、严格应用白名单、启动确认和 socket protect 失败处理。当前工程从官方 Mihomo 派生核心进行 OHOS 适配，完整 wrapper 已编译、链接通过并生成验证用 ARM64 共享库；尚无经过整包与设备验收的交付版本。

应用与 wrapper 位于本仓；核心和专用 Go 编译器由独立仓维护。工程设计、环境与验证步骤见 [clashbox-meta](https://github.com/Ghroth6/clashbox-meta)（私有，需权限）；标准工作空间中的协调仓位于 `../../meta`。自动化协作入口见 [AGENTS.md](AGENTS.md)。

2026-10-04 已实现订阅信息序列化、同一快照连接数、异步 DNS 清理、平台 TUN 所有权及命名 TUN 拒绝、四种 geodata 的实际路径校验。外部控制器采用官方嵌入模式，配置/规则变更与进程重启须经应用入口；查询和代理选择接口保留。

普通代理监听现由 wrapper 控制：配置应用先使用不开放代理端口的运行副本，TUN 确认后恢复传统、命名和 tunnel 入口；停止时先关闭入口和已跟踪连接，再关闭系统 TUN。命名监听每次从原配置重建，避免重复启停累积旧句柄。系统 VPN 配置明确拒绝 iptables.enable，避免额外路由接管及执行器在禁用 tproxy 端口后退出；拒绝不改写配置原文。`proxy_core/src/flclash/compat` 的宿主验证入口为 `pwsh -NoProfile -File scripts/test-wrapper-compat.ps1`，使用工程清单固定的官方核心；测试使用独立临时核心目录及回环端口。

代理列表、选择、测速与事件使用稳定 provider 身份，同组裸名歧义明确报错；只有选择成功才持久化。身份绑定进入核心选择组，避免检查与选择之间或后续 provider 刷新改选其他同名节点；同 provider 同名新对象可延续选择。Selector 目标失效时拒绝连接，URLTest/Fallback 保留健康判断及自动切换，列表显示实际节点身份及不可用状态。流量可查询实际代理出口或全量，已关闭连接仍计入累计。核心事件支持注销，IPC 订阅等待 ready、按 JSON 行解析并有界重连，短连接历史最多保存 1000 条。NAPI 事件采用独立 C owner 管理环境寿命，投递失败注销并记录诊断，调用方需要重新注册及刷新快照。

NetworkKit 从已连接的 INTERNET 非 VPN 网络取得真实接口、地址/前缀、MTU、路由及默认网络 DNS，发布完整且带代际的快照。首次采集失败阻止服务就绪；缺省或离线不采用公共 DNS fallback。未知 ifindex 明确为 0，不使用 netId 冒充。被动订阅仅监听默认网络，其它接口在默认网络事件或显式 start 时刷新。监视器随服务生存，服务销毁时清空快照。

代理监听启动现在检查核心实际 bind 结果，任一步失败都尝试关闭整个代理入口集合并返回失败；已登记对象的 close 错误保留诊断及重试所有权。底层部分构造失败会回滚，若 Close 自身失败则返回错误，不能保证资源已释放。startListener 仅在系统 TUN 已就绪且本批代理入口全部成功后返回 true。

OHOS 启动在第一个 await 前取得原生代际令牌，IPC 携带令牌，停止后才接收的旧请求也会被拒绝。StopTun 先取消保护等待及其 IPC，再取得构造锁关闭监听和 TUN；构造期间保持系统 fd 生存边界，避免依赖阻塞在同步 NAPI 后的 ArkTS ACK。protect 通道断开仅清理所属实例，旧通道不能关闭新 TUN，也不能为新 VPN 重试或确认旧保护请求。应用停止直接调用 StopTun，避免先调用持同一锁的 stopListener。

首次 TUN 预留前保留配置初始化网络活动；之后没有有效保护 owner 就拒绝新出站。DNS/controller/provider 后台任务仍未完整停止，系统 VPN destroy 尚无同步完成确认，因此停机后的后台下载也未恢复。完整 ArkTS/HAP、设备加载、实际 fd 回收及真机反复启停仍待验收。当前输入、产物与测试证据以协调仓 README、设计及检查点为准；tests/native-lifecycle 的实际函数宿主测试由协调仓 prepare-bridge-tests.py 准备，OS 构造器使用可控替身。

新增平台快照和事件客户端的宿主测试为 `tests/network-snapshot.test.mjs` 与 `tests/stream-subscription.test.mjs`；使用工程声明的 IDE Node 运行 `--test`。这些测试执行生产模型和状态机，不替代 SDK/ArkTS 编译与设备运行。

配置导入回归另见 `tests/profile-import.test.mjs`，覆盖普通 JSON、流式 YAML 原字节保留，以及识别出的资源包仍执行结构和路径限制。使用 IDE Node 运行 `scripts/check-client-arkts.mjs` 可检查 `NetworkSnapshot.ets` 与 `ProfileImport.ets`：它直接调用本机 SDK 的编译器和 ArkTS 1.1 检查器，分别输出诊断阶段、错误及警告；默认 SDK 位于 DevEco 标准安装目录，也可传入 `--sdk-ets-dir` 指定 SDK 的 `openharmony/ets`。该入口不依赖历史探针或其它工作副本，不生成 HAP。

本机验证批次集中在 `local/runs/<批次名>`，可重建缓存放在 `local/cache`，并行工作副本保留在 `local/worktrees`。`test-wrapper-compat.ps1` 每次生成新的批次目录，内含测试副本、实际 app/core 提交和输入哈希的 `inputs.json`、日志与 `result.json`；详细信息保留在批次内，工程检查点只需链接结论和证据入口。缓存可以重新生成，验证记录的保留方式见协调仓的 `docs/material-layout.md`。

## 上游原始说明

以下保留上游原文，描述其自身产品与安装方式，不表示本派生分支已经提供相同的核心或安装包。

# ClashBox

#### 介绍

ClashBox是一个HarmonyOS NEXT(OpenHarmony)平台的代理软件，使用改版的ClashMate内核

注意：本仓库仅包含前端部分，改版的后端部分暂不开源

#### 食用方法

需要使用Auto-installer(https://github.com/likuai2010/auto-installer/)进行安装
