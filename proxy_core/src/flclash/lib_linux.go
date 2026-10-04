//go:build ohos && cgo

package main

import "C"
import (
	"core/compat"
	"core/platform"
	"core/state"
	t "core/tun"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/metacubex/mihomo/component/dialer"

	"github.com/metacubex/mihomo/component/process"
	"github.com/metacubex/mihomo/constant"

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
	runTime       *time.Time
	errBlocked    = errors.New("blocked")
	keepaliveStop chan struct{}
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
	runLock.Lock()
	defer runLock.Unlock()
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
				func() {
					runLock.Lock()
					defer runLock.Unlock()
					// A queued tick from an earlier TUN must not act on a new run.
					if tunListener == nil || keepaliveStop != stop || currentConfig == nil {
						return
					}
					if string(currentConfig.General.Mode) == "direct" {
						return
					}
					if compat.ConnectionCount(statistic.DefaultManager.Snapshot()) > 0 {
						go handleHealthCheckAll()
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
	keepaliveStop = nil
}

func GetRunTime() string {
	runLock.Lock()
	defer runLock.Unlock()
	if runTime == nil {
		return "clash服务未启动"
	}
	return strconv.FormatInt(runTime.UnixMilli(), 10)
}
func ConfigInited() string {
	runLock.Lock()
	defer runLock.Unlock()
	if currentConfig != nil {
		return "true"
	}
	return "false"
}

func StopTun() {
	runLock.Lock()
	defer runLock.Unlock()
	stopKeepalive()
	stopCoreEvents()
	isRunning = false
	stopListeners()
	handleCloseConnectionsUnLock()
	runTime = nil
	if tunListener != nil {
		_ = tunListener.Close()
		tunListener = nil
	}
	removeSocketHook()
	compat.FlushDNS()
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
	runLock.Lock()
	defer runLock.Unlock()
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

// Legacy DNS-only callers receive an explicit error; network publication is atomic.
func UpdateSystemDns(_ string) error {
	return errors.New("publish a complete platform network snapshot instead of DNS alone")
}
func SetInterfaces(raw string) error { return compat.PublishNetworkSnapshot(raw) }
