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
	tunListener *sing_tun.Listener
	tunOwner    *tunSession // protected by runLock, including a pending constructor
	// TUN constructors/Close do not expose a reliable retry contract. Keep an
	// uncertain result sticky instead of closing a possibly recycled fd again.
	tunCleanupErr      error
	listenerCleanupErr error // Forwarding Stop retains failed resources for retry.
	protectionOwner    atomic.Pointer[tunSession]
	counter            int64 = 0
	processMap         ProcessMap
	runTime            *time.Time
	errBlocked         = errors.New("blocked")
	keepaliveStop      chan struct{}
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

func StartTUN(fd int, owner *tunSession, markSocket func(Fd)) error {
	if fd <= 0 {
		return errors.New("invalid system TUN descriptor")
	}
	// Keep construction serialized with Stop so the caller cannot destroy and
	// recycle its system fd while the constructor still uses it. Stop cancels
	// protect before acquiring this lock, so construction cannot await an ArkTS
	// ACK blocked behind synchronous NAPI Stop.
	runLock.Lock()
	defer runLock.Unlock()
	if err := errors.Join(tunCleanupErr, listenerCleanupErr); err != nil {
		return fmt.Errorf("native cleanup is unresolved: %w", err)
	}
	if currentConfig == nil {
		return errors.New("configuration is not loaded")
	}
	if tunListener != nil || tunOwner != nil || !tunSessions.Reserve(owner, markSocket) {
		return errors.New("TUN start was cancelled or a TUN is already running")
	}
	tunOwner = owner
	protectionOwner.Store(owner)
	target, err := compat.PrepareForwarding(owner.ctx)
	if err != nil {
		tunSessions.Cancel(owner)
		protectionOwner.CompareAndSwap(owner, nil)
		return fmt.Errorf("prepare forwarding: %w", err)
	}
	options := currentConfig.General.Tun
	options.DNSHijack = append([]string(nil), options.DNSHijack...)
	listener, err := t.Start(fd, options.Device, options.Stack, options.DNSHijack, target)
	if err != nil {
		tunSessions.Cancel(owner)
		protectionOwner.CompareAndSwap(owner, nil)
		// The constructor may have consumed fd and attempted rollback before
		// returning no listener. Its error cannot prove either release or a leak.
		// Keep this owner even without a handle; system destroy alone does not
		// establish that every native resource was released.
		tunListener = listener
		tunCleanupErr = fmt.Errorf("TUN construction failed; native cleanup cannot be confirmed: %w", err)
		return tunCleanupErr
	}
	tunListener = listener
	if tunOwner != owner || !tunSessions.Commit(owner) {
		tunSessions.Cancel(owner)
		protectionOwner.CompareAndSwap(owner, nil)
		closeErr := closeTunLocked()
		if closeErr == nil {
			tunOwner = nil
		}
		return errors.Join(errors.New("TUN start was cancelled"), closeErr)
	}
	if err := compat.ResumeManagementNetwork(); err != nil {
		return fmt.Errorf("resume protected management network: %w", err)
	}
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
					// The keepalive policy applies only to this forwarding run.
					// Management/internal requests must not be closed by this scan.
					generation := compat.ForwardingGeneration()
					active := false
					n := 0
					statistic.DefaultManager.Range(func(c statistic.Tracker) bool {
						metadata := c.Info().Metadata
						if generation == 0 || metadata == nil || metadata.ForwardingGeneration != generation {
							return true
						}
						active = true
						// 仅关闭已空闲超过 120s 的连接
						if time.Since(c.LastActivity()) > 120*time.Second {
							_ = c.Close()
							n++
						}
						return true
					})
					if active {
						go handleHealthCheckAll()
					}
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

func StopTun() error {
	// Invalidate requests captured before Stop, even when their handler has not
	// reached StartTUN yet. This also releases pending protect waits immediately.
	tunSessions.CancelAll()
	if tunSessions.ProtectionRequired() {
		compat.CancelManagementNetwork()
	}
	compat.CancelForwarding()
	runLock.Lock()
	defer runLock.Unlock()
	tunSessions.Cancel(tunOwner)
	return stopTunLocked()
}

func StopTunOwner(owner *tunSession) error {
	// A disconnected old stream must never shut down a replacement session.
	tunSessions.Cancel(owner)
	runLock.Lock()
	defer runLock.Unlock()
	if tunOwner != owner || owner == nil {
		return nil
	}
	compat.CancelManagementNetwork()
	return stopTunLocked()
}

func stopTunLocked() error {
	protectionOwner.CompareAndSwap(tunOwner, nil)
	stopKeepalive()
	isRunning = false
	listenerCleanupErr = stopListeners()
	runTime = nil
	tunErr := closeTunLocked()
	err := errors.Join(listenerCleanupErr, tunErr)
	if err == nil {
		tunOwner = nil
	}
	return err
}

// Called only under runLock. Retrying the aggregate TUN Close could close a
// descriptor already recycled after a partial failure, or return nil merely
// because an inner component considers itself closed. Neither proves cleanup.
func closeTunLocked() error {
	if tunCleanupErr != nil {
		return tunCleanupErr
	}
	if tunListener == nil {
		return nil
	}
	if err := tunListener.Close(); err != nil {
		tunCleanupErr = fmt.Errorf("TUN close failed; native cleanup cannot be confirmed: %w", err)
		return tunCleanupErr
	}
	tunListener = nil
	return nil
}

func SetFdMap(fd C.long) { acknowledgeProtectedSocket(int64(fd)) }

func initSocketHook() {
	dialer.DefaultSocketHook = func(network, address string, conn syscall.RawConn) error {
		if platform.ShouldBlockConnection() {
			return errBlocked
		}
		owner := protectionOwner.Load()
		if owner == nil && !tunSessions.ProtectionRequired() {
			return nil
		}
		if !owner.Valid() {
			return errProtectionUnavailable
		}
		// DIRECT traffic also needs protection from being recaptured by this TUN.
		return protectOutboundSocketUntil(conn, owner.request, 5*time.Second, owner.done)
	}
}

func init() {
	// The function pointer is immutable after package initialization; sessions
	// change through an atomic owner pointer. After bootstrap, no owner is blocked.
	initSocketHook()
	compat.EnableManagementNetwork()
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
	runLock.Lock()
	defer runLock.Unlock()
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
	_ = updateOptionState(paramsString)
}

// Legacy DNS-only callers receive an explicit error; network publication is atomic.
func UpdateSystemDns(_ string) error {
	return errors.New("publish a complete platform network snapshot instead of DNS alone")
}
func SetInterfaces(raw string) error { return compat.PublishNetworkSnapshot(raw) }
