# ClashBox 个人派生分支

本仓保留 ClashBox 应用上游代码，并维护配置原文与资源保留、严格应用白名单、启动确认和 socket protect 失败处理。当前工程从官方 Mihomo 派生核心进行 OHOS 适配，完整 wrapper 已编译、链接通过并生成验证用 ARM64 共享库；尚无经过整包与设备验收的交付版本。

应用与 wrapper 位于本仓；核心和专用 Go 编译器由独立仓维护。工程设计、环境与验证步骤见 [clashbox-meta](https://github.com/Ghroth6/clashbox-meta)（私有，需权限）；标准工作空间中的协调仓位于 `../../meta`。自动化协作入口见 [AGENTS.md](AGENTS.md)。

2026-10-04 已实现订阅信息序列化、同一快照连接数、异步 DNS 清理、平台 TUN 所有权及命名 TUN 拒绝、四种 geodata 的实际路径校验。外部控制器采用官方嵌入模式，配置/规则变更与进程重启须经应用入口；查询和代理选择接口保留。

普通代理监听现由 wrapper 控制：配置应用先使用不开放代理端口的运行副本，TUN 确认后恢复传统、命名和 tunnel 入口；停止时先关闭入口和已跟踪连接，再关闭系统 TUN。命名监听每次从原配置重建，避免重复启停累积旧句柄。系统 VPN 配置明确拒绝 iptables.enable，避免额外路由接管及执行器在禁用 tproxy 端口后退出；拒绝不改写配置原文。`proxy_core/src/flclash/compat` 的宿主验证入口为 `pwsh -NoProfile -File scripts/test-wrapper-compat.ps1`，使用工程清单固定的官方核心；测试使用独立临时核心目录及回环端口。

代理列表、选择、测速与事件使用稳定 provider 身份，同组裸名歧义明确报错；只有选择成功才持久化。流量可查询实际代理出口或全量，已关闭连接仍计入累计。核心事件支持注销，IPC 订阅等待 ready、按 JSON 行解析并有界重连，短连接历史最多保存 1000 条。NAPI 事件采用独立 C owner 管理环境寿命，投递失败注销并记录诊断，调用方需要重新注册及刷新快照。

NetworkKit 从已连接的 INTERNET 非 VPN 网络取得真实接口、地址/前缀、MTU、路由及默认网络 DNS，发布完整且带代际的快照。首次采集失败阻止服务就绪；缺省或离线不采用公共 DNS fallback。未知 ifindex 明确为 0，不使用 netId 冒充。被动订阅仅监听默认网络，其它接口在默认网络事件或显式 start 时刷新。监视器随服务生存，服务销毁时清空快照。

正常入口生命周期之外仍有待办：核心绑定错误仍只记录日志，部分构造失败回滚、已发送的原生启动请求取消、protect 通道断开清理、DNS/controller 与后台任务停机均未闭环；startListener 返回 true 不是所有端口已绑定的确认。尚未完成完整 ArkTS/HAP 构建、设备加载或真机验收。当前输入、产物与测试证据以协调仓 README、设计及检查点为准。

新增平台快照和事件客户端的宿主测试为 `tests/network-snapshot.test.mjs` 与 `tests/stream-subscription.test.mjs`；使用工程声明的 IDE Node 运行 `--test`。这些测试执行生产模型和状态机，不替代 SDK/ArkTS 编译与设备运行。

## 上游原始说明

以下保留上游原文，描述其自身产品与安装方式，不表示本派生分支已经提供相同的核心或安装包。

# ClashBox

#### 介绍

ClashBox是一个HarmonyOS NEXT(OpenHarmony)平台的代理软件，使用改版的ClashMate内核

注意：本仓库仅包含前端部分，改版的后端部分暂不开源

#### 食用方法

需要使用Auto-installer(https://github.com/likuai2010/auto-installer/)进行安装
