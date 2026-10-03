# ClashBox 个人派生分支

本仓保留 ClashBox 应用上游代码，并维护配置原文与资源保留、严格应用白名单、启动确认和 socket protect 失败处理。当前工程从官方 Mihomo 派生核心进行 OHOS 适配，尚无可交付的原生核心或 HAP。

应用与 wrapper 位于本仓；核心和专用 Go 编译器由独立仓维护。工程设计、环境与验证步骤见 [clashbox-meta](https://github.com/Ghroth6/clashbox-meta)（私有，需权限）；标准工作空间中的协调仓位于 `../../meta`。自动化协作入口见 [AGENTS.md](AGENTS.md)。

## 上游原始说明

以下保留上游原文，描述其自身产品与安装方式，不表示本派生分支已经提供相同的核心或安装包。

# ClashBox

#### 介绍

ClashBox是一个HarmonyOS NEXT(OpenHarmony)平台的代理软件，使用改版的ClashMate内核

注意：本仓库仅包含前端部分，改版的后端部分暂不开源

#### 食用方法

需要使用Auto-installer(https://github.com/likuai2010/auto-installer/)进行安装