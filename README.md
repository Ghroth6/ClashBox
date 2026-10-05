# ClashBox 个人派生分支

本仓保留 ClashBox 应用上游代码，并维护配置原文与资源保留、严格应用白名单、启动确认和 socket protect 失败处理。独立自用调试身份为 `org.ghroth.clashbox`，显示名保留 ClashBox；卡片唤起使用同一身份。导入与分享沿用 `clash://install-config`、`clashbox://install-config` 协议，不声明未经验证的上游 HTTPS 域名关联。

当前工程从官方 Mihomo 派生核心进行 OHOS 适配，已有完整 OHOS ARM64 wrapper、ArkTS/HAP 打包及调试签名验证结果。保存版本的源码组合、产物与验证边界以协调仓 README 和检查点为准；设备授权匹配、加载及平台行为尚未验收，不能作为已通过真机验证的交付版本。

应用与 wrapper 位于本仓；核心和专用 Go 编译器由独立仓维护。工程设计、环境与验证步骤见 [clashbox-meta](https://github.com/Ghroth6/clashbox-meta)（私有，需权限）；标准工作空间中的协调仓位于 `../../meta`。自动化协作入口见 [AGENTS.md](AGENTS.md)。

2026-10-04 已实现订阅信息序列化、同一快照连接数、异步 DNS 清理、平台 TUN 所有权及命名 TUN 拒绝、四种 geodata 的实际路径校验。外部控制器采用官方嵌入模式，配置/规则变更与进程重启须经应用入口；查询和代理选择接口保留。

代理入口现由 wrapper 的 forwarding 生命周期控制：配置应用时保留内部 resolver，外部 DNS/DoH 和代理端口保持关闭；TUN 与传统、命名、tunnel 入口绑定同一运行代次，构造和绑定完成后才开放根请求。停止先取消该代转发，再关闭入口并等待专属请求和连接收尾；管理连接与事件继续保留。失败保留清理记录，后续启动不能跳过它。命名监听每次从原配置重建，避免重复启停累积旧句柄。系统 VPN 配置明确拒绝 iptables.enable，避免额外路由接管及执行器在禁用 tproxy 端口后退出；拒绝不改写配置原文。`proxy_core/src/flclash/compat` 的宿主验证入口为 `pwsh -NoProfile -File scripts/test-wrapper-compat.ps1`，使用工程清单固定的官方核心或显式完整候选 SHA；测试使用独立临时核心目录及回环端口。

代理列表、选择、测速与事件使用稳定 provider 身份，同组裸名歧义明确报错；只有选择成功才持久化。身份绑定进入核心选择组，避免检查与选择之间或后续 provider 刷新改选其他同名节点；同 provider 同名新对象可延续选择。Selector 目标失效时拒绝连接，URLTest/Fallback 保留健康判断及自动切换，列表显示实际节点身份及不可用状态。流量可查询实际代理出口或全量，已关闭连接仍计入累计。核心事件支持注销，IPC 订阅等待 ready、按 JSON 行解析并有界重连，短连接历史最多保存 1000 条。NAPI 事件采用独立 C owner 管理环境寿命，投递失败注销并记录诊断，调用方需要重新注册及刷新快照。

NetworkKit 从已连接的 INTERNET 非 VPN 网络取得真实接口、地址/前缀、MTU、路由及默认网络 DNS，发布完整且带代际的快照。首次采集失败阻止服务就绪；缺省或离线不采用公共 DNS fallback。未知 ifindex 明确为 0，不使用 netId 冒充。被动订阅仅监听默认网络，其它接口在默认网络事件或显式 start 时刷新。监视器随服务生存，服务销毁时清空快照。

代理监听启动现在检查核心实际 bind 结果，任一步失败都尝试关闭整个代理入口集合并返回失败；已登记对象的 close 错误保留诊断及重试所有权。底层部分构造失败会回滚，若 Close 自身失败则返回错误，不能保证资源已释放。startListener 仅在系统 TUN 已就绪且本批代理入口全部成功后返回 true。

OHOS 启动在第一个 await 前取得原生代际令牌，IPC 携带令牌，停止后才接收的旧请求也会被拒绝。StopTun 先取消保护等待及其 IPC，再取得构造锁关闭监听和 TUN；构造期间保持系统 fd 生存边界，避免依赖阻塞在同步 NAPI 后的 ArkTS ACK。protect 通道断开仅清理所属实例，旧通道不能关闭新 TUN，也不能为新 VPN 重试或确认旧保护请求。应用停止直接调用 StopTun，避免先调用持同一锁的 stopListener。

Start/Stop RPC 现在返回异步操作结果，包含运行代次、状态、失败阶段和清理错误；客户端序号阻止迟到的旧命令改变新状态。平台保留系统 create/destroy、在途 protect 和保护通道的归属，等待实际结果；超时、释放失败或原生构造回滚无法确认时保留 CleanupFailed，阻止新启动。可重试的系统 destroy 和监听关闭由后续 Stop 重试；无法确认已消费 fd 的原生构造/Close 错误不能靠重复 Close 清零，当前只能保留诊断与阻断。同步 NAPI 阻塞不能由 ArkTS 超时中断。

UI 只在收到实际成功结果后发布启停事件；失败保留最后确认状态和待清理意图，不再自动发起第二次启动。停止可取消准备中的启动，旧回执及迟到运行时间查询不能恢复运行显示。配置重置、重启及运行中的配置重载同样先等待停止结果；原生入口拒绝替换存活 TUN 的配置。

配置替换与进程 Shutdown 先取消旧配置所属的 provider、测速、地理库和 MRS 请求，再等待实际完成；代理 Stop 保留这些管理任务。旧请求在排队前绑定实际对象，迟到结果不能写入新配置；关闭失败或等待超时保留诊断与阻断。完整解析会临时修改核心全局设置，必须在旧任务退出后执行：深度校验失败保留原文，但保持停止，需成功重载才能启动；语法及平台入口校验仍在退休前。旧 `is-patch` 请求也执行完整替换。`configuration_lifecycle.go` 的宿主测试运行实际 parser/executor，平台监听和事件呈现使用替身；下载器与管理请求归属直接在 compat 测试。

平台 create 前登记原生 owner，关闭管理网络准入、取消并等待旧网络尝试及物理连接，然后才修改系统路径；原生保护通道与 TUN 就绪后开放受保护的新管理网络代次。Stop 取消本代网络尝试，配置任务和调度器仍保留。系统 destroy 真正确认后携原 owner 回执恢复普通管理联网；迟到旧回执不能释放新 owner，等待或关闭失败保留状态。系统已确认不存在 VPN 时可以恢复管理网络，未知原生 TUN 清理仍独立阻止下次启动。Profile/MRS 原生下载及其 DNS socket 遵循同一保护和网络代次，过渡取消后不得提交半成品；系统 NetworkKit HTTP 尚不能证明相同保护/完成合同，因此原生失败明确返回，当前不启用该兜底。Profile 下载、验证使用独立临时文件，版本与 URL 变更拒绝迟到响应，验证后原子替换，避免跨 Profile 并发或旧订阅覆盖后来的编辑。核心已接入失败候选与历史节点清理，协议依赖的确定性退出边界以协调仓当前设计为准。完整 ArkTS/HAP 打包与调试签名已有成功批次；设备加载、系统 destroy 与原生 Close 的 fd 关系及真机反复启停仍待验收。当前输入、产物与测试证据以协调仓 README、设计及检查点为准；tests/native-lifecycle 的实际函数宿主测试由协调仓 prepare-bridge-tests.py 准备，OS 构造器使用可控替身。

平台快照、事件、异步服务与 UI/RPC 的宿主测试位于 `tests/*.test.mjs`；使用工程声明的 IDE Node 运行 `--test`。生命周期测试执行生产状态机、服务与客户端方法，覆盖延迟、取消、失败、重复和迟到回执；系统 API 使用替身，不替代设备运行。

订阅并发、迟到结果、手动编辑和原生失败的真实文件回归见 `tests/profile-update.test.mjs`。配置导入回归另见 `tests/profile-import.test.mjs`，覆盖普通 JSON、流式 YAML 原字节保留，以及识别出的资源包仍执行结构和路径限制。使用 IDE Node 运行 `scripts/check-client-arkts.mjs` 可检查 NetworkSnapshot、ProfileImport 及六个生命周期/RPC 模块：它直接调用本机 SDK 编译器、ArkUI 声明和 ArkTS 1.1 检查器，分别输出诊断阶段、错误及警告；默认 SDK 位于 DevEco 标准安装目录，也可传入 `--sdk-ets-dir` 指定 SDK 的 `openharmony/ets`。该入口不依赖历史探针或其它工作副本，不检查全部 UI 页面，不生成 HAP。

本机验证批次集中在 `local/runs/<批次名>`，可重建缓存放在 `local/cache`，并行工作副本保留在 `local/worktrees`。`test-wrapper-compat.ps1` 每次生成新的批次目录，内含测试副本、实际 app/core 提交和输入哈希的 `inputs.json`、日志与 `result.json`；详细信息保留在批次内，工程检查点只需链接结论和证据入口。缓存可以重新生成，验证记录的保留方式见协调仓的 `docs/material-layout.md`。

## 上游原始说明

以下保留上游原文，描述其自身产品与安装方式，不表示本派生分支已经提供相同的核心或安装包。

# ClashBox

#### 介绍

ClashBox是一个HarmonyOS NEXT(OpenHarmony)平台的代理软件，使用改版的ClashMate内核

注意：本仓库仅包含前端部分，改版的后端部分暂不开源

#### 食用方法

需要使用Auto-installer(https://github.com/likuai2010/auto-installer/)进行安装
