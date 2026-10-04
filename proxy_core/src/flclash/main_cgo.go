//go:build cgo && ohos

package main

//#include "bridge.h"
import "C"
import (
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"

	napi "github.com/likuai2010/ohos-napi"
	"github.com/likuai2010/ohos-napi/entry"
	"github.com/likuai2010/ohos-napi/js"
	"github.com/metacubex/mihomo/log"
)

func initClash(env js.Env, this js.Value, args []js.Value) any {
	homeDirStr, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	return handleInitClash(homeDirStr)
}

func startTun(env js.Env, this js.Value, args []js.Value) any {
	tunFd, _ := napi.GetValueInt32(env.Env, args[0].Value)
	tsfn := env.CreateThreadsafeFunction(args[1], "startTun")
	owner := tunSessions.NewSession(tunSessions.Generation())
	err := StartTUN(int(tunFd), owner, func(fd Fd) {

		tsfn.Call(env.ValueOf(fd.Id), env.ValueOf(fd.Value))
	})
	if err != nil {
		tunSessions.Cancel(owner)
		return err.Error()
	}
	return ""
}
func stopTun(env js.Env, this js.Value, args []js.Value) any {
	StopTun()
	return nil
}

func getTunStartToken(env js.Env, this js.Value, args []js.Value) any {
	return tunSessions.StartToken()
}

func validateConfig(env js.Env, this js.Value, args []js.Value) any {
	paramsString, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	bytes := []byte(paramsString)
	promise := env.NewPromise()
	go func() {
		promise.Resolve(handleValidateConfig(bytes))
	}()
	return promise
}

func updateConfig(env js.Env, this js.Value, args []js.Value) any {
	paramsString, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	fmt.Println("updateConfig requested")
	promise := env.NewPromise()
	bytes := []byte(paramsString)
	go func() {
		promise.Resolve(handleUpdateConfig(bytes))
	}()
	return promise
}

func getProxies(env js.Env, this js.Value, args []js.Value) any {
	return handleGetProxies()
}

func changeProxy(env js.Env, this js.Value, args []js.Value) any {
	paramsString, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	promise := env.NewPromise()
	handleChangeProxy(paramsString, func(value string) {
		promise.Resolve(value)
	})
	return promise
}

func getTraffic(env js.Env, this js.Value, args []js.Value) any {
	onlyProxy := true
	if len(args) > 0 {
		onlyProxy, _ = napi.GetValueBool(env.Env, args[0].Value)
	}
	return handleGetTraffic(onlyProxy)
}
func getTotalTraffic(env js.Env, this js.Value, args []js.Value) any {
	onlyProxy := true
	if len(args) > 0 {
		onlyProxy, _ = napi.GetValueBool(env.Env, args[0].Value)
	}
	return handleGetTotalTraffic(onlyProxy)
}
func resetTraffic(env js.Env, this js.Value, args []js.Value) any {
	handleResetTraffic()
	return nil
}
func forceGc(env js.Env, this js.Value, args []js.Value) any {
	handleForceGc()
	return nil
}
func asyncTestDelay(env js.Env, this js.Value, args []js.Value) any {
	paramsString, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	promise := env.NewPromise()
	handleAsyncTestDelay(paramsString, func(value string) {
		promise.Resolve(value)
	})
	return promise
}
func getExternalProviders(env js.Env, this js.Value, args []js.Value) any {
	return handleGetExternalProviders()
}
func getExternalProvider(env js.Env, this js.Value, args []js.Value) any {
	externalProviderName, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	return handleGetExternalProvider(externalProviderName)
}
func updateGeoData(env js.Env, this js.Value, args []js.Value) any {
	geoType, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	geoName, _ := napi.GetValueStringUtf8(env.Env, args[1].Value)
	promise := env.NewPromise()
	handleUpdateGeoData(geoType, geoName, func(value string) {
		promise.Resolve(value)
	})
	return promise
}
func updateExternalProvider(env js.Env, this js.Value, args []js.Value) any {
	providerName, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	promise := env.NewPromise()
	handleUpdateExternalProvider(providerName, func(value string) {
		promise.Resolve(value)
	})
	return promise
}

func sideLoadExternalProvider(env js.Env, this js.Value, args []js.Value) any {
	providerName, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	dataChar, _ := napi.GetValueStringUtf8(env.Env, args[1].Value)
	data := []byte(dataChar)
	promise := env.NewPromise()
	handleSideLoadExternalProvider(providerName, data, func(value string) {
		promise.Resolve(value)
	})
	return promise
}
func getConnections(env js.Env, this js.Value, args []js.Value) any {
	return handleGetConnections()
}

func closeConnections(env js.Env, this js.Value, args []js.Value) any {
	return handleCloseConnections()
}

func closeConnection(env js.Env, this js.Value, args []js.Value) any {
	connectionId, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	return handleCloseConnection(connectionId)
}

func startLog(env js.Env, this js.Value, args []js.Value) any {
	tsfn := env.CreateThreadsafeFunction(args[0], "startLog")
	handleStartLog(func(value string) {
		tsfn.Call(env.ValueOf("startLog"), env.ValueOf(value))
	})
	return nil
}

func stopLog(env js.Env, this js.Value, args []js.Value) any {
	handleStopLog()
	return nil
}
func getCountryCode(env js.Env, this js.Value, args []js.Value) any {
	ip, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	promise := env.NewPromise()
	handleGetCountryCode(ip, func(value string) {
		promise.Resolve(value)
	})
	return promise
}
func getMemory(env js.Env, this js.Value, args []js.Value) any {
	promise := env.NewPromise()
	handleGetMemory(func(value string) {
		promise.Resolve(value)
	})
	return promise
}
func updateDns(env js.Env, this js.Value, args []js.Value) any {
	promise := env.NewPromise()
	promise.Resolve(UpdateSystemDns("").Error())
	return promise
}
func publishNetworkSnapshot(env js.Env, this js.Value, args []js.Value) any {
	if len(args) != 1 {
		return "complete network snapshot required"
	}
	raw, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	if err := SetInterfaces(raw); err != nil {
		return err.Error()
	}
	return ""
}
func setState(env js.Env, this js.Value, args []js.Value) any {
	paramsString, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	err := updateOptionState(paramsString)
	if err != nil {
		return nil
	}
	return nil
}
func setProcessMap(env js.Env, this js.Value, args []js.Value) any {
	paramsString, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	return SetProcessMap(paramsString)
}

func getVpnOptions(env js.Env, this js.Value, args []js.Value) any {
	return GetVpnOptions()
}
func getCurrentProfileName(env js.Env, this js.Value, args []js.Value) any {
	return GetCurrentProfileName()
}

func setFdMap(env js.Env, this js.Value, args []js.Value) any {
	fdInt, _ := napi.GetValueInt32(env.Env, args[0].Value)
	acknowledgeProtectedSocket(int64(fdInt))
	return nil
}

var messageHandlerMu sync.Mutex
var messageHandler unsafe.Pointer

func registerMessage(env js.Env, this js.Value, args []js.Value) any {
	if len(args) == 0 {
		return "callback required"
	}
	handler := C.flclash_events_create(unsafe.Pointer(env.Env), unsafe.Pointer(args[0].Value))
	if handler == nil {
		return "cannot register event callback"
	}
	messageHandlerMu.Lock()
	if messageHandler != nil {
		C.flclash_events_close(messageHandler)
	}
	messageHandler = handler
	messageHandlerMu.Unlock()
	return nil
}
func unregisterMessage(env js.Env, this js.Value, args []js.Value) any {
	messageHandlerMu.Lock()
	if messageHandler != nil {
		C.flclash_events_close(messageHandler)
		messageHandler = nil
	}
	messageHandlerMu.Unlock()
	return nil
}
func getRequestList(env js.Env, this js.Value, args []js.Value) any {
	json, _ := json.Marshal(requestHistory.Snapshot())
	return env.ValueOf(string(json))
}

func clearRequestList(env js.Env, this js.Value, args []js.Value) any {
	requestHistory.Clear()
	return env.ValueOf("")
}
func startListener(env js.Env, this js.Value, args []js.Value) any {
	return handleStartListener()
}
func stopListener(env js.Env, this js.Value, args []js.Value) any {
	handleStopListener()
	return env.ValueOf("")
}
func startIpc(env js.Env, this js.Value, args []js.Value) any {
	path, _ := napi.GetValueStringUtf8(env.Env, args[0].Value)
	go startIpcProxy(path)
	return env.ValueOf("")
}

func init() {
	entry.Export("initClash", js.AsCallback(initClash))
	entry.Export("startTun", js.AsCallback(startTun))
	entry.Export("setFdMap", js.AsCallback(setFdMap))
	entry.Export("stopTun", js.AsCallback(stopTun))
	entry.Export("getTunStartToken", js.AsCallback(getTunStartToken))
	entry.Export("forceGc", js.AsCallback(forceGc))
	entry.Export("validateConfig", js.AsCallback(validateConfig))
	entry.Export("updateConfig", js.AsCallback(updateConfig))
	entry.Export("getTraffic", js.AsCallback(getTraffic))
	entry.Export("getTotalTraffic", js.AsCallback(getTotalTraffic))
	entry.Export("resetTraffic", js.AsCallback(resetTraffic))
	entry.Export("getProxies", js.AsCallback(getProxies))
	entry.Export("changeProxy", js.AsCallback(changeProxy))
	entry.Export("asyncTestDelay", js.AsCallback(asyncTestDelay))
	entry.Export("getConnections", js.AsCallback(getConnections))
	entry.Export("closeConnections", js.AsCallback(closeConnections))
	entry.Export("closeConnection", js.AsCallback(closeConnection))
	entry.Export("updateExternalProvider", js.AsCallback(updateExternalProvider))
	entry.Export("sideLoadExternalProvider", js.AsCallback(sideLoadExternalProvider))
	entry.Export("getExternalProviders", js.AsCallback(getExternalProviders))
	entry.Export("getVpnOptions", js.AsCallback(getVpnOptions))
	entry.Export("getCurrentProfileName", js.AsCallback(getCurrentProfileName))
	entry.Export("setProcessMap", js.AsCallback(setProcessMap))
	entry.Export("updateGeoData", js.AsCallback(updateGeoData))
	entry.Export("startListener", js.AsCallback(startListener))
	entry.Export("stopListener", js.AsCallback(stopListener))
	entry.Export("startIpc", js.AsCallback(startIpc))

	entry.Export("updateDns", js.AsCallback(updateDns))
	entry.Export("publishNetworkSnapshot", js.AsCallback(publishNetworkSnapshot))
	entry.Export("startLog", js.AsCallback(startLog))
	entry.Export("stopLog", js.AsCallback(stopLog))
	entry.Export("registerMessage", js.AsCallback(registerMessage))
	entry.Export("unregisterMessage", js.AsCallback(unregisterMessage))
	entry.Export("getRequestList", js.AsCallback(getRequestList))
	entry.Export("clearRequestList", js.AsCallback(clearRequestList))

	entry.Export("getCountryCode", js.AsCallback(getCountryCode))
	entry.Export("getMemory", js.AsCallback(getMemory))

}

func sendMessage(message Message) {
	res, err := message.Json()
	if err != nil {
		return
	}
	messages.Publish(res)
	messageHandlerMu.Lock()
	defer messageHandlerMu.Unlock()
	if messageHandler != nil && C.flclash_events_send(messageHandler, C.CString(res)) != 0 {
		C.flclash_events_close(messageHandler)
		messageHandler = nil
		log.Warnln("Native event subscription closed after delivery failure; re-register and refresh snapshots")
	}
}

func main() {
}
