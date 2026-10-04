import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import vm from 'node:vm';
import test from 'node:test';

const read = file => readFileSync(new URL(`../${file}`, import.meta.url), 'utf8');
const rpcFile = 'proxy_core/src/main/ets/rpc/SocketProxyService.ets';
const viewFile = 'entry/src/main/ets/entryability/ClashViewModel.ets';
function slice(file, signature) {
  const source = read(file), begin = source.indexOf(signature);
  assert.ok(begin >= 0, signature);
  return source.slice(begin, source.indexOf('\n  }', begin) + 4).replace(/^  private /, '  ');
}
function compile(source, globals = {}) {
  return vm.runInNewContext(stripTypeScriptTypes(source), { console, Error, JSON, Promise, setTimeout, clearTimeout, ...globals });
}
const parserSource = read('proxy_core/src/main/ets/rpc/VpnOperationClient.ts').replace(/^import .*;\r?\n/gm, '').replace(/^export /gm, '');
const parser = compile(`${parserSource}\n({decodeVpnOperationResult, requireVpnOperationState})`);
const result = (state, error = '', cleanupErrors = []) => ({ generation: 1, state, stage: 'test', error, cleanupErrors });
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
const tick = () => new Promise(resolve => setImmediate(resolve));

for (const raw of [true, false, 'false', 'null', '{}', '{bad', JSON.stringify({...result('Stopped'), generation: -1}), JSON.stringify({...result('Stopped'), cleanupErrors: [7]})]) {
  test(`invalid lifecycle response cannot become a truthy success: ${raw}`, () => {
    assert.throws(() => parser.decodeVpnOperationResult(raw));
  });
}
test('cleanup failure and mismatched terminal state propagate instead of boolean success', () => {
  for (const value of [result('CleanupFailed', '', ['destroy failed']), result('Stopping'), result('Stopped', 'cancelled')]) {
    const decoded = parser.decodeVpnOperationResult(JSON.stringify(value));
    assert.throws(() => parser.requireVpnOperationState(decoded, 'Stopped'));
  }
  assert.equal(parser.requireVpnOperationState(parser.decodeVpnOperationResult(JSON.stringify(result('Stopped'))), 'Stopped'), true);
});
function rpcHarness() {
  const h = compile(`class Client {
    ${slice(rpcFile, '  async startVpnOperation(')}
    ${slice(rpcFile, '  async stopVpnOperation(')}
    ${slice(rpcFile, '  async startClash(')}
    ${slice(rpcFile, '  async stopClash(')}
  }\nnew Client()`, { ...parser, ClashRpcType: { startClash: 13, stopClash: 14 } });
  h.vpnCommandSequence = 0; h.vpnClientId = 'test-client';
  return h;
}
test('client preserves structured failure and never overwrites newer stop with a late start ACK', async () => {
  const h = rpcHarness(), start = deferred(), calls = [];
  h.sendMessageRequest = (method, params) => { calls.push([method, ...params]); return method === 13 ? start.promise : Promise.resolve(JSON.stringify(result('Stopped'))); };
  const first = h.startClash();
  assert.equal(await h.stopClash(), true);
  start.resolve(JSON.stringify(result('Running'))); await first;
  assert.equal(h.lastVpnOperation.state, 'Stopped');
  assert.deepEqual(calls.map(v => [...v]), [[13, 'test-client', 1], [14, 'test-client', 2]]);
  h.sendMessageRequest = async () => JSON.stringify(result('CleanupFailed', 'stop', ['system destroy failed']));
  await assert.rejects(h.stopClash(), /system destroy failed/);
  assert.equal(h.lastVpnOperation.state, 'CleanupFailed');
});
test('transport string errors retain their reason and do not reuse a previous successful response', async () => {
  const h = rpcHarness();
  h.lastVpnOperation = result('Stopped');
  h.sendMessageRequest = async () => { throw '请求超时'; };
  await assert.rejects(h.startClash(), /请求超时/);
  assert.equal(h.lastVpnOperation, undefined);
  await assert.rejects(h.stopClash(), /请求超时/);
});
function viewHarness() {
  const source = read(viewFile), begin = source.indexOf('  vpnStarted = false'), end = source.indexOf('  // 启动前', begin);
  const events = [], cards = [];
  const h = compile(`class View {
    ${source.slice(begin, end)}
    ${slice(viewFile, '  async ReStartVpn(')}
    ${slice(viewFile, '  async StartVpn(')}
    ${slice(viewFile, '  private async startVpnWithIntent(')}
    ${slice(viewFile, '  async StopVpn(')}
    ${slice(viewFile, '  private async stopVpnWithResult(')}
    ${slice(viewFile, '  private async ensureCoreEvents(')}
    ${slice(viewFile, '  private cancelCoreEvents(')}
  }\nnew View()`, {
    EventHub: { sendEvent: event => events.push(event), on: () => {} },
    EventKey: { StartedClash: 'started', StopedClash: 'stopped', StopedClashEntry: 'stopped-entry', checkIpInfo: 'ip', TestDelay: 'delay', FetchProxyGroup: 'loaded' },
    cardManager: { pushCartProxyMode: value => cards.push(value), pushCartVpnServiceTime: () => {} },
    hilog: { info: () => {} }, PROXY_STARTED_DURATION_INIT_VALUE: '00:00', number2Time: String,
  });
  h.socketProxy = { lastVpnOperation: undefined, startClash: async () => true, stopClash: async () => true };
  h.loadConfig = async () => {}; h.loadVpnOptions = async () => {}; h.getRuntime = async () => 123;
  h.coreEventsGeneration = 0; h.delayMap = new Map();
  h.proxyStartedTimer = { start: () => {}, reset: () => events.push('timer-reset') };
  return { h, events, cards };
}

test('forwarding stop retains management events, while service replacement cancels the old stream', async () => {
  const { h, events } = viewHarness(); let callback, unsubscribed = 0;
  h.socketProxy.registerMessage = async observer => { callback = observer; return () => { unsubscribed++; }; };
  await h.ensureCoreEvents();
  await h.StopVpn();
  callback(JSON.stringify({type: 'delay', data: {name: 'proxy', value: 42}}));
  callback(JSON.stringify({type: 'loaded', data: {success: false, name: 'provider', error: 'network unavailable'}}));
  assert.equal(unsubscribed, 0); assert.equal(h.delayMap.get('proxy').delay, 42);
  assert.ok(events.includes('delay')); assert.ok(events.includes('loaded'));
  h.cancelCoreEvents();
  const before = events.length;
  callback(JSON.stringify({type: 'loaded', data: {success: true}}));
  assert.equal(events.length, before); assert.equal(unsubscribed, 1);
});

test('configuration replacement while running uses acknowledged restart and cannot bypass failed cleanup', async () => {
  const method = slice(viewFile, '  async loadConfig(');
  const h = compile(`class View { ${method} }\nnew View()`);
  let restarts = 0;
  h.vpnStarted = true;
  h.ReStartVpn = async () => { restarts++; throw new Error('destroy failed'); };
  await assert.rejects(h.loadConfig(false), /destroy failed/); assert.equal(restarts, 1);
  h.vpnCleanupRequired = true;
  await assert.rejects(h.loadConfig(false), /清理尚未完成/); assert.equal(restarts, 1);
});
test('stop has no success effects before acknowledgement, and failure retains cleanup intent for retry', async () => {
  const { h, events, cards } = viewHarness(), stop = deferred();
  h.vpnStarted = true; h.vpnDesiredRunning = true;
  h.socketProxy.stopClash = () => stop.promise;
  const pending = h.StopVpn();
  assert.equal(h.vpnDesiredRunning, false); assert.equal(h.vpnStarted, true);
  assert.deepEqual(events, []); assert.deepEqual(cards, []);
  stop.reject(new Error('destroy failed')); await assert.rejects(pending, /destroy failed/);
  assert.deepEqual(events, []); assert.equal(h.needsVpnStop(), true); assert.equal(h.canRecoverVpn(), false);
  await assert.rejects(h.StartVpn(), /清理尚未完成/);
  h.socketProxy.stopClash = async () => true;
  await h.StopVpn();
  assert.equal(h.vpnStarted, false); assert.equal(h.needsVpnStop(), false);
  assert.ok(events.includes('stopped')); assert.deepEqual(cards, [false]);
});
test('stop during configuration preparation prevents a late start RPC', async () => {
  const { h, events } = viewHarness(), config = deferred(); let starts = 0;
  h.loadConfig = () => config.promise;
  h.socketProxy.startClash = async () => { starts++; return true; };
  const started = h.StartVpn();
  await h.StopVpn(); config.resolve();
  await assert.rejects(started, /已取消/);
  assert.equal(starts, 0); assert.equal(events.includes('started'), false);
});
test('a start acknowledgement received after explicit stop cannot restore UI running state', async () => {
  const { h, events } = viewHarness(), ack = deferred();
  h.socketProxy.startClash = () => ack.promise;
  const started = h.StartVpn(); await tick();
  await h.StopVpn(); ack.resolve(true);
  await assert.rejects(started, /已取消/);
  assert.equal(h.vpnStarted, false); assert.equal(events.includes('started'), false);
});
test('cold configuration failure and failed start never trigger an automatic second start', async () => {
  for (const configFailure of [true, false]) {
    const { h } = viewHarness(); let starts = 0;
    h.loadConfig = async () => { if (configFailure) throw new Error('config failed'); };
    h.socketProxy.startClash = async () => { starts++; throw new Error('start failed'); };
    await assert.rejects(h.StartVpn());
    assert.equal(starts, configFailure ? 0 : 1); assert.equal(h.vpnStarted, false);
  }
});
test('failed optional runtime lookup does not retract a successful start', async () => {
  const { h, events } = viewHarness();
  h.getRuntime = async () => { throw new Error('runtime unavailable'); };
  await h.StartVpn();
  assert.equal(h.vpnStarted, true); assert.equal(h.vpnDesiredRunning, true);
  assert.equal(events.filter(value => value === 'started').length, 1);
  await h.StartVpn(); assert.equal(events.filter(value => value === 'started').length, 1);
});
for (const rejectRuntime of [false, true]) test(`stop during optional runtime lookup cannot revive the card or timer (reject=${rejectRuntime})`, async () => {
  const { h, cards } = viewHarness(), runtime = deferred();
  h.getRuntime = () => runtime.promise;
  const start = h.StartVpn(); await tick();
  await h.StopVpn();
  if (rejectRuntime) runtime.reject(new Error('runtime unavailable')); else runtime.resolve(123);
  await assert.rejects(start, /已取消/);
  assert.equal(h.vpnStarted, false); assert.deepEqual(cards, [false]);
});
test('restart cannot reload or start after a failed stop, and preserves the original error', async () => {
  const { h } = viewHarness(); let loads = 0;
  h.vpnStarted = true; h.vpnDesiredRunning = true;
  h.socketProxy.stopClash = async () => { throw new Error('destroy stage failure'); };
  h.loadConfig = async () => { loads++; };
  await assert.rejects(h.ReStartVpn(), /destroy stage failure/); assert.equal(loads, 0);
});
test('explicit stop arriving during restart cleanup cancels the restart intention', async () => {
  const { h } = viewHarness(), ack = deferred(); let starts = 0;
  h.vpnStarted = true; h.vpnDesiredRunning = true;
  h.socketProxy.stopClash = () => ack.promise;
  h.socketProxy.startClash = async () => { starts++; return true; };
  const restart = h.ReStartVpn(), stopped = h.StopVpn(); ack.resolve(true);
  await stopped; await assert.rejects(restart, /重启已取消/); assert.equal(starts, 0);
});
test('concurrent start and stop requests share their pending operations', async () => {
  const { h } = viewHarness(), ack = deferred(); let starts = 0, stops = 0;
  h.socketProxy.startClash = () => { starts++; return ack.promise; };
  const one = h.StartVpn(), two = h.StartVpn(); await tick();
  ack.resolve(true); await Promise.all([one, two]); assert.equal(starts, 1);
  const stop = deferred(); h.socketProxy.stopClash = () => { stops++; return stop.promise; };
  const three = h.StopVpn(), four = h.StopVpn(); stop.resolve(true);
  await Promise.all([three, four]); assert.equal(stops, 1);
});
test('reset waits for stop and leaves configuration untouched on cleanup failure', async () => {
  const stopped = deferred(), writes = [];
  const method = slice('entry/src/main/ets/entryability/AppState.ets', '  static async ResetConfig(');
  const h = compile(`class State { ${method} }\nState`, {
    ClashViewModel: { StopVpn: () => stopped.promise },
    AppState: { ResetClashConfig: () => writes.push('reset') },
    AppStorage: { set: () => writes.push('write') }, AppConfig: class {},
  });
  const pending = h.ResetConfig({}); assert.deepEqual(writes, []);
  stopped.reject(new Error('not stopped')); await assert.rejects(pending, /not stopped/); assert.deepEqual(writes, []);
});
test('cold card stop initializes only RPC context, bypasses configuration checks and propagates cleanup failure', async () => {
  let calls = 0;
  const socketProxy = { context: undefined, init(context) { this.context = context; } };
  const source = slice('entry/src/main/ets/entryability/EntryAbility.ets', '  private async CardClashCoreEvent(');
  const h = compile(`class Card { ${source} }\nnew Card()`, {
    hilog: { info: () => {} }, ProxyActionType: { START: 1 },
    ClashViewModel: {
      socketProxy,
      StopVpn: async () => { assert.equal(socketProxy.context?.filesDir, '/app/files'); calls++; throw new Error('destroy failed'); },
      vpnConfigIsLoad: async () => { throw new Error('must not check configuration to stop'); },
    },
  });
  h.context = { filesDir: '/app/files' };
  await assert.rejects(h.CardClashCoreEvent(0), /destroy failed/); assert.equal(calls, 1);
});
