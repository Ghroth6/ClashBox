import { requireAllowlist } from './AllowlistPolicy';
import { PlatformNetworkMonitor } from './NetworkSnapshot';
import { vpnExtension, socket } from '@kit.NetworkKit';
import {
  startTun, stopTun, getTunStartToken, setFdMap, getVpnOptions, startLog, getProxies, getTraffic,
  getTotalTraffic,
  getExternalProviders,
  asyncTestDelay,
  updateConfig,
  initClash,
  changeProxy,
  forceGc,
  updateExternalProvider,
  getCountryCode,
  updateGeoData,
  sideLoadExternalProvider,
  getConnections,
  closeConnections,
  closeConnection,
  validateConfig,
  registerMessage,
  getRequestList,
  clearRequestList,
  startListener
} from 'libflclash.so';
import { Address, AddressWithPrefix, CommonVpnService, cidrToRoute, VpnConfig } from './CommonVpnService';
import { JSON, util } from '@kit.ArkTS';
import { RpcRequest, RpcResult } from './RpcRequest';
import { ClashRpcType } from './IClashManager';
import { ConnectionInfo, LogInfo, Proxy, Provider, ProxyGroup, ProxyMode, ProxyType, Traffic } from '../models/Common';
import { getHome, getProfilePath } from '../appPath';
import { ClashConfig, Tun, UpdateConfigParams } from '../models/ClashConfig';
import { readFile, readFileUri, readText } from '../fileUtils';

export interface AccessControl {
  mode: string
  acceptList: string[]
  rejectList: string[]
  isFilterSystemApp: boolean
}
export interface VpnOptions {
  enable: boolean,
  port: number,
  ipv4Address: string,
  ipv6Address: string,
  ipv6?: boolean,
  accessControl: AccessControl,
  systemProxy: boolean,
  allowBypass: boolean,
  routeAddress: string[],
  bypassDomain: string[],
  dnsServerAddress: string,
  /** 最大传输单元: 缺省 1400 */
  mtu?: number,
}


export class FlClashVpnService extends CommonVpnService {
  vpnConnection: vpnExtension.VpnConnection | undefined
  public configPath: string = ""
  protectSocketPath: string = ""
  private clashSocket: socket.LocalSocket | undefined
  private textDecoder: util.TextDecoder = new util.TextDecoder()
  private networkMonitor: PlatformNetworkMonitor = new PlatformNetworkMonitor()

  override async onRemoteMessageRequest(client: socket.LocalSocketConnection, message: socket.LocalSocketMessageInfo): Promise<void> {
    let request = JSON.parse(this.textDecoder.decodeToString(new Uint8Array(message.message))) as RpcRequest
    let code = request.method
    let params = request.params
    try {
      let result = await this.onRemoteMessage(code, params)
      this.sendClient(client, JSON.stringify({ result: result, error: undefined }))
    } catch (e) {
      console.error(`socket stub ${code} result: `, e.message ?? e, e.stack)
      this.sendClient(client, JSON.stringify({ error: e.message ?? e }))
    }
  }
  /** 防止多次调用 stopVpn 导致 stopTun 重复执行 */
  private isStopped: boolean = true
  private isStarting: boolean = false
  private startGeneration: number = 0

  async onRemoteMessage(code: number, data: (string | number | boolean)[]): Promise<string | number | boolean> {
    switch (code) {
      case ClashRpcType.startClash: {
        return await this.startVpn()
      }
      case ClashRpcType.stopClash: {
        this.stopVpn()
        return true
      }
      default: {
        return "不支持当前操作"
      }
    }
  }

  ParseConfig(): VpnConfig {
    let vpnConfig = new VpnConfig();
    let option = JSON.parse(getVpnOptions()) as VpnOptions
    // 根据 ipv6 开关决定是否启用 IPv6 地址和路由
    const vpnIpv6Enabled = option.ipv6 !== false
    if (option.ipv6Address == undefined || option.ipv6Address == "") {
      option.ipv6Address = "fdfe:dcba:9876::1/126"
    }
    if (option.routeAddress == undefined) {
      option.routeAddress = []
    }
    if (option.ipv4Address != "") {
      const ips = option.ipv4Address.split("/")
      console.debug("tunIp ", ips)
      const prefixLength = ips.length > 1 ? parseInt(ips[1]) : 30
      vpnConfig.addresses[0] = new AddressWithPrefix(new Address(ips[0], 1), prefixLength)
      vpnConfig.isIPv4Accepted = true
    }
    if (vpnIpv6Enabled && option.ipv6Address != "") {
      const ips = option.ipv6Address.split("/")
      const prefixLength = ips.length > 1 ? parseInt(ips[1]) : 126
      vpnConfig.addresses.push(new AddressWithPrefix(new Address(ips[0], 2), prefixLength))
      vpnConfig.isIPv6Accepted = true
    }
    const routeAddresses: string[] = []
    const addRouteAddress = (cidr: string) => {
      if (cidr != "" && !routeAddresses.includes(cidr)) {
        routeAddresses.push(cidr)
      }
    }
    // Explicit defaults avoid OHOS auto-generating only fe80::/derived IPv6 coverage.
    option.routeAddress?.forEach(addRouteAddress)
    if (option.ipv4Address != "") {
      addRouteAddress("0.0.0.0/0")
    }
    if (vpnIpv6Enabled && option.ipv6Address != "") {
      addRouteAddress("::/0")
    }
    routeAddresses.forEach((cidr) => {
      const route = cidrToRoute(cidr)
      if (route != null) {
        vpnConfig.routes.push(route)
      }
    })
    const trusted = requireAllowlist(true, option.accessControl?.mode ?? '', option.accessControl?.acceptList ?? []);
    vpnConfig.trustedApplications = trusted;
    // Never attach blockedApplications together with trustedApplications.
    if (option.dnsServerAddress && option.dnsServerAddress != "") {
      vpnConfig.dnsAddresses = [option.dnsServerAddress]
    } else {
      vpnConfig.dnsAddresses = ["172.19.0.2"]
    }
    // ★ MTU: 优先使用用户配置(1280-65535), 缺省 1400
    vpnConfig.mtu = (option.mtu && option.mtu >= 1280 && option.mtu <= 65535) ? option.mtu : 1400
    if (option.systemProxy || option.allowBypass) {
      // TODO ohos 不支持
      // not use option.bypassDomain option.port
    }
    console.debug("vpnConfig", JSON.stringify(vpnConfig))
    return vpnConfig;
  }
  override async startVpn(): Promise<boolean> {
    if (this.isStarting) return false
    if (!this.isStopped) return true
    this.isStarting = true
    this.isStopped = false
    const generation = ++this.startGeneration
    try {
      const config = this.ParseConfig()
      const nativeToken = getTunStartToken()
      await this.networkMonitor.start()
      if (generation !== this.startGeneration) return false
      const tunFd = await super.getTunFd(config)
      if (generation !== this.startGeneration) return false
      if (tunFd <= 0) {
        throw new Error('系统未返回有效的 TUN 文件描述符')
      }
      await this.startClash(tunFd, nativeToken)
      if (generation !== this.startGeneration) return false
      // The protect channel and native TUN must be ready before proxy ingress.
      // The core returns actual bind failures and rolls back partial ingress.
      if (!startListener()) {
        throw new Error('代理监听启动失败，请检查端口占用和监听配置')
      }
      return true
    } catch (error) {
      console.error("ClashVPN  error ", error)
      if (generation === this.startGeneration) this.stopVpn()
      return false
    } finally {
      this.isStarting = false
    }
  }

  async startClash(tunFd: number, nativeToken: string): Promise<void> {
    const previousSocket = this.clashSocket
    this.clashSocket = undefined
    previousSocket?.off('message')
    previousSocket?.close()
    let tcp: socket.LocalSocket = socket.constructLocalSocketInstance();
    this.clashSocket = tcp
    const socketPath = this.context?.filesDir + '/clash_go.sock'
    await new Promise<void>((resolve, reject) => {
      let pending = ''
      let settled = false
      let nativeReady = false
      const timer = setTimeout(() => {
        if (!settled) {
          settled = true
          reject(new Error('等待原生 TUN 启动确认超时'))
        }
      }, 10000)
      const fail = (error: Error) => {
        if (!settled) {
          settled = true
          clearTimeout(timer)
          reject(error)
        }
      }
      const disconnected = (error: Error) => {
        if (this.clashSocket !== tcp) return
        fail(error)
        if (nativeReady) this.stopVpn()
      }
      tcp.on('close', () => disconnected(new Error('原生 TUN 保护通道已关闭')))
      tcp.on('error', (error: Error) => disconnected(error))
      tcp.on('message', (value: socket.LocalSocketMessageInfo) => {
        pending += this.textDecoder.decodeToString(new Uint8Array(value.message))
        if (pending.length > 65536) {
          fail(new Error('原生 TUN 响应过长'))
          return
        }
        let boundary = pending.indexOf('EOF')
        while (boundary >= 0) {
          const element = pending.substring(0, boundary)
          pending = pending.substring(boundary + 3)
          if (element !== '') {
            try {
              const result = JSON.parse(element) as RpcResult
              if (result.error) {
                fail(new Error(result.error))
              } else if (result.result === 'tun-ready') {
                if (!settled) {
                  settled = true
                  nativeReady = true
                  clearTimeout(timer)
                  resolve()
                }
              } else {
                this.handleProtectMessage(element, tcp)
              }
            } catch (error) {
              fail(error as Error)
            }
          }
          boundary = pending.indexOf('EOF')
        }
      })
      tcp.connect({ address: { address: socketPath }, timeout: 1000 }).then(async () => {
        if (settled || this.clashSocket !== tcp) {
          fail(new Error('原生 TUN 启动已取消'))
          return
        }
        await tcp.send({ data: JSON.stringify({ method: ClashRpcType.startClash, params: [tunFd, nativeToken] }) })
      }).catch((error: Error) => fail(error))
    })
  }

  /**
   * 单个 protect 消息的异步处理：解析 fd → protect（带重试）→ setFdMap。
   * 每个 fd 独立执行。失败或通道更换不发送 ACK，由原生端拒绝未受保护的出站连接。
   */
  private handleProtectMessage(element: string, owner: socket.LocalSocket): void {
    if (this.clashSocket !== owner) return
    if (element == "") return
    try {
      let json = JSON.parse(element) as RpcResult
      let fd = JSON.parse(json.result as string) as Fd
      this.protectWithRetry(fd.value, 3, owner).then(() => {
        if (this.clashSocket === owner) setFdMap(fd.id)
      }).catch((protectErr: Error) => {
        // 重试仍失败：打 hilog 点便于确认后台 protect 是否被系统中断（hilog | grep ClashVPN protect）
        console.error("ClashVPN protect failed after retry, skipping setFdMap for fd.id=" + fd.id, protectErr.message, element)
      })
    } catch (e) {
      console.error("ClashVPN message parse error", (e as Error).message, element)
    }
  }

  stopVpn() {
    this.startGeneration++
    if (this.isStopped) return
    this.isStopped = true
    // Native Stop first cancels pending protect requests, then closes ingress
    // and TUN. Calling stopListener here first could block behind its constructor.
    stopTun()
    const previousSocket = this.clashSocket
    this.clashSocket = undefined
    previousSocket?.off('message')
    previousSocket?.close()
    super.stopVpn()
  }

  shutdown() {
    this.stopVpn()
    this.networkMonitor.stop()
  }

  /**
   * protect 带重试：后台 Extension 进程受限时 vpnConnection.protect 可能偶发失败，
   * 失败重试可避免出站 fd 未保护导致流量回环（直连断网）
   */
  private async protectWithRetry(fd: number, retries: number, owner: socket.LocalSocket): Promise<void> {
    let lastErr: Error | undefined = undefined
    for (let i = 0; i < retries; i++) {
      if (this.clashSocket !== owner) throw new Error('原生 TUN 保护请求已取消')
      try {
        await this.protect(fd)
        return
      } catch (e) {
        lastErr = e as Error
        await new Promise<void>((resolve) => {
          setTimeout(resolve, 100)
        })
      }
    }
    throw lastErr ?? new Error('protect failed')
  }
  override async init() {
    initClash(await getHome(this.context), "1.0.0")
    await this.networkMonitor.start()
  }
}

export interface Fd {
  id: number
  value: number
}

export function ParseProxyGroup(mode: ProxyMode, result: string): ProxyGroup[] {
  if (result == null || result.length === 0)
    return []
  const map = JSON.parse(result) as Record<string, Record<string, string | string[] | boolean | number | undefined>>
  const global = map[ProxyMode.Global]
  let groupNames: string[] = (global?.["all"] as string[] | undefined) ?? []
  if (mode == ProxyMode.Global) {
    groupNames = ["GLOBAL", ...groupNames]
  } else if (mode == ProxyMode.Rule) {
    // keep groupNames as is
  } else {
    groupNames = []
  }
  groupNames = groupNames.filter(e => {
    const proxy = map[e]
    if (!proxy)
      return false
    return ["Selector", "URLTest", "Fallback", "LoadBalance", "Relay"].indexOf(proxy["type"] as string) > -1
  })
  const groupsRaw: (ProxyGroup | null)[] = groupNames.map((groupName) => {
    const group = map[groupName];
    if (!group) {
      return null;
    }
    const rawAll = group["all"]
    const allNames: string[] = Array.isArray(rawAll) ? rawAll as string[] : []
    const proxies: Proxy[] = []
    for (const n of allNames) {
      const rawProxy = map[n];
      if (!rawProxy) {
        continue;
      }
      const proxyName = (rawProxy["name"] as string | undefined) ?? n;
      const proxyType = (rawProxy["type"] as string | undefined) ?? "Reject";
      proxies.push({
        name: proxyName,
        type: proxyType as ProxyType,
        display: (rawProxy["display"] as string | undefined) ?? "",
        id: rawProxy["id"] as string | undefined,
        g: rawProxy["g"] as string | undefined,
        icon: rawProxy["icon"] as string | undefined,
        latency: rawProxy["latency"] as number | undefined,
      })
    }
    return {
      name: (group["name"] as string) ?? "",
      now: (group["now"] as string) ?? "",
      type: (group["type"] as ProxyType) ?? ProxyType.Reject,
      display: (group["display"] as string) ?? "",
      hidden: group["hidden"] == true,
      icon: group["icon"] as string | undefined,
      proxies: proxies
    } as ProxyGroup
  })
  return groupsRaw.filter(g => g != null) as ProxyGroup[];
}
