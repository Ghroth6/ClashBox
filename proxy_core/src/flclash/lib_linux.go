//go:build ohos && cgo

package main

import "C"
import (
	"core/platform"
	"core/state"
	t "core/tun"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"net"

	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/iface"
	"github.com/metacubex/mihomo/component/process"
	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/dns"
	"github.com/metacubex/mihomo/listener/sing_tun"
	"github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

type ProcessMap struct {
	m sync.Map
}

var (
	tunListener   *sing_tun.Listener
	counter       int64 = 0
	processMap    ProcessMap
	tunLock       sync.Mutex
	runTime       *time.Time
	errBlocked    = errors.New("blocked")
	keepaliveStop chan struct{}
	keepaliveOnce sync.Once
)

func (cm *ProcessMap) Store(key int64, value string) {
	cm.m.Store(key, value)
}

func (cm *ProcessMap) Load(key int64) (string, bool) {
	value, ok := cm.m.Load(key)
	if !ok || value == nil {
		return "", false
	}
	return value.(string), true
}

func StartTUN(fd int, markSocket func(Fd)) error {
	tunLock.Lock()
	defer tunLock.Unlock()
	if fd <= 0 {
		return errors.New("invalid system TUN descriptor")
	}
	if currentConfig == nil {
		return errors.New("configuration is not loaded")
	}
	if tunListener != nil {
		return errors.New("TUN is already running")
	}
	initSocketHook(markSocket)
	listener, err := t.Start(fd, currentConfig.General.Tun.Device, currentConfig.General.Tun.Stack, currentConfig.General.Tun.DNSHijack)
	if err != nil {
		removeSocketHook()
		return err
	}
	tunListener = listener
	now := time.Now()
	runTime = &now
	startKeepalive()
	return nil
}

func startKeepalive() {
	keepaliveOnce.Do(func() {
		keepaliveStop = make(chan struct{})
	})
	// 先停止已有保活
	stopKeepalive()
	keepaliveStop = make(chan struct{})
	stop := keepaliveStop
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		log.Infoln("[Keepalive] TUN 保活 goroutine 已启动")
		for {
			select {
			case <-ticker.C:
				// 直连模式下不需要健康检查和空闲连接清理
				if currentConfig != nil && string(currentConfig.General.Mode) == "direct" {
					continue
				}
				// 仅在有活跃连接时才做健康检查, 无流量时跳过以降低功耗
				connSnapshot := statistic.DefaultManager.Snapshot()
				if connSnapshot != nil && connSnapshot.ConnectionCount() > 0 {
					go handleHealthCheckAll()
				}
				func() {
					runLock.Lock()
					defer runLock.Unlock()
					if tunListener == nil {
						return
					}
					// 关闭所有空闲连接，强制 NAT 重新建立映射
					n := 0
					statistic.DefaultManager.Range(func(c statistic.Tracker) bool {
						// 仅关闭已空闲超过 120s 的连接
						if time.Since(c.LastActivity()) > 120*time.Second {
							_ = c.Close()
							n++
						}
						return true
					})
					if n > 0 {
						log.Infoln("[Keepalive] 已关闭 %d 个空闲连接", n)
					}
				}()
			case <-stop:
				log.Infoln("[Keepalive] TUN 保活 goroutine 已停止")
				return
			}
		}
	}()
}

func stopKeepalive() {
	if keepaliveStop != nil {
		select {
		case <-keepaliveStop:
			// 已关闭
		default:
			close(keepaliveStop)
		}
	}
	keepaliveOnce = sync.Once{}
}

func GetRunTime() string {
	tunLock.Lock()
	defer tunLock.Unlock()
	if runTime == nil {
		return "clash服务未启动"
	}
	return strconv.FormatInt(runTime.UnixMilli(), 10)
}
func ConfigInited() string {
	if currentConfig != nil {
		return "true"
	}
	return "false"
}

func StopTun() {
	tunLock.Lock()
	defer tunLock.Unlock()
	stopKeepalive()
	runTime = nil
	if tunListener != nil {
		_ = tunListener.Close()
		tunListener = nil
	}
	removeSocketHook()
	dns.FlushCacheWithDefaultResolver()
}

func SetFdMap(fd C.long) { acknowledgeProtectedSocket(int64(fd)) }

func initSocketHook(markSocket func(Fd)) {
	dialer.DefaultSocketHook = func(network, address string, conn syscall.RawConn) error {
		if platform.ShouldBlockConnection() {
			return errBlocked
		}
		// DIRECT traffic also needs protection from being recaptured by this TUN.
		return protectOutboundSocket(conn, markSocket, 5*time.Second)
	}
}

func removeSocketHook() {
	dialer.DefaultSocketHook = nil
}

func init() {
	process.DefaultPackageNameResolver = func(metadata *constant.Metadata) (string, error) {
		if metadata == nil {
			return "", process.ErrInvalidNetwork
		}
		id := atomic.AddInt64(&counter, 1)

		timeout := time.After(200 * time.Millisecond)

		// SendMessage(Message{
		// 	Type: ProcessMessage,
		// 	Data: Process{
		// 		Id:       id,
		// 		Metadata: metadata,
		// 	},
		// })

		for {
			select {
			case <-timeout:
				return "", errors.New("package resolver timeout")
			default:
				value, exists := processMap.Load(id)
				if exists {
					return value, nil
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	}
}

func SetProcessMap(s string) string {
	paramsString := s
	go func() {
		var processMapItem = &ProcessMapItem{}
		err := json.Unmarshal([]byte(paramsString), processMapItem)
		if err == nil {
			processMap.Store(processMapItem.Id, processMapItem.Value)
		}
	}()
	return ""
}

func GetCurrentProfileName() string {
	if state.CurrentState == nil {
		return ""
	}
	return state.CurrentState.CurrentProfileName
}

func GetVpnOptions() string {
	tunLock.Lock()
	defer tunLock.Unlock()
	port := 7980
	if currentConfig != nil {
		port = currentConfig.General.MixedPort
	}
	options := state.AndroidVpnOptions{
		Enable:           state.CurrentState.Enable,
		Port:             port,
		Ipv4Address:      state.CurrentState.TunIp,
		Ipv6Address:      state.GetIpv6Address(),
		AccessControl:    state.CurrentState.AccessControl,
		SystemProxy:      state.CurrentState.SystemProxy,
		AllowBypass:      state.CurrentState.AllowBypass,
		RouteAddress:     state.CurrentState.RouteAddress,
		BypassDomain:     state.CurrentState.BypassDomain,
		DnsServerAddress: state.GetDnsServerAddress(),
		Mtu:              state.CurrentState.Mtu,
	}
	data, err := json.Marshal(options)
	if err != nil {
		fmt.Println("Error:", err)
		return ""
	}
	return string(data)
}

func SetState(s *C.char) {
	paramsString := C.GoString(s)
	err := json.Unmarshal([]byte(paramsString), state.CurrentState)
	if err != nil {
		return
	}
}

func UpdateDns(s *C.char) {
	dnsList := C.GoString(s)
	go func() {
		log.Infoln("[DNS] updateDns %s", dnsList)
		dns.UpdateSystemDNS(strings.Split(dnsList, ","))
		dns.FlushCacheWithDefaultResolver()
	}()
}

func UpdateSystemDns(dnsList string) error {
	log.Infoln("[DNS] updateDns %s", dnsList)
	go func() {
		log.Infoln("[DNS] updateDns %s", dnsList)
		dns.UpdateSystemDNS(strings.Split(dnsList, ","))
		dns.FlushCacheWithDefaultResolver()
	}()
	return nil
}

type NetIpMacInfo struct {
	IpAddress  NetAddress `json:"ipAddress"`
	Iface      string     `json:"iface"`
	MacAddress string     `json:"macAddress"`
}
type NetAddress struct {
	Address string `json:"address"` // IP地址
	Family  int    `json:"family"`  // 地址族：4(IPv4)或6(IPv6)
	Port    int    `json:"port"`    // 端口号（如果有）
}

func (info *NetIpMacInfo) ToNetInterface() (*net.Interface, error) {
	// 解析 MAC 地址
	var mac net.HardwareAddr
	if info.MacAddress != "" {
		var err error
		mac, err = net.ParseMAC(info.MacAddress)
		if err != nil {
			return nil, fmt.Errorf("parse MAC address failed: %w", err)
		}
	}

	// 获取接口索引（通过接口名）
	var index int
	if info.Iface != "" {
		iface, err := net.InterfaceByName(info.Iface)
		if err == nil && iface != nil {
			index = iface.Index
		}
	}

	return &net.Interface{
		Index:        index,
		MTU:          1500, // 默认值，你可能需要从其他地方获取
		Name:         info.Iface,
		HardwareAddr: mac,
		Flags:        getInterfaceFlags(info), // 需要实现这个函数
	}, nil
}
func getInterfaceFlags(info *NetIpMacInfo) net.Flags {
	var flags net.Flags

	// 如果 MAC 地址存在，通常接口是启用的
	if info.MacAddress != "" {
		flags |= net.FlagUp
		flags |= net.FlagBroadcast
		flags |= net.FlagMulticast
	}

	// 检查是否为回环接口
	if info.Iface == "lo" || info.Iface == "lo0" {
		flags |= net.FlagLoopback
	}
	return flags
}

func SetInterfaces(paramsString string) error {
	var interfaces []net.Interface
	var infos []NetIpMacInfo
	err := json.Unmarshal([]byte(paramsString), &infos)
	if err != nil {
		return err
	}
	seen := make(map[string]bool) // 去重
	for _, info := range infos {
		if seen[info.Iface] {
			continue
		}
		ifa, err := info.ToNetInterface()
		if err != nil {
			continue // 或者返回错误
		}

		if ifa != nil {
			interfaces = append(interfaces, *ifa)
			seen[info.Iface] = true
		}
	}
	iface.SetNetInterfaces(interfaces)
	return nil
}
