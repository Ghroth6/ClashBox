import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import { setImmediate } from 'node:timers/promises';
import vm from 'node:vm';
import test from 'node:test';

const root = new URL('../proxy_core/src/main/ets/rpc/', import.meta.url);
const files = ['VpnLifecycle', 'CommonVpnService', 'FlClashVpnService'];
const sources = files.map(name => {
  const source = readFileSync(new URL(name + '.ets', root), 'utf8');
  assert.equal(source, readFileSync(new URL(name + '.ts', root), 'utf8'), name + ' mirror');
  return source;
});
const policy = readFileSync(new URL('AllowlistPolicy.ets', root), 'utf8');
function code(source) {
  return stripTypeScriptTypes(source.replace(/^import[\s\S]*?;\r?\n/gm, '').replace(/^export /gm, ''), { mode: 'strip' });
}
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
async function until(predicate) {
  for (let i = 0; i < 100; i++) { if (predicate()) return; await setImmediate(); }
  assert.fail('asynchronous condition did not settle');
}
function fixture(options = {}) {
  const calls = [], connections = [], sockets = [];
  const config = { ipv4Address: '172.19.0.1/30', ipv6Address: '', ipv6: false,
    accessControl: { mode: 'AcceptSelected', acceptList: ['com.example.selected'], rejectList: [] } };
  class LocalSocket {
    events = new Map();
    on(name, listener) { this.events.set(name, listener); }
    off(name) { this.events.delete(name); }
    emit(name, value) { this.events.get(name)?.(value); }
    async connect() { calls.push('ipc-connect'); if (options.connect) await options.connect(); }
    async send(data) {
      calls.push('native-start');
      this.request = JSON.parse(data.data);
      if (options.native) await options.native();
      const result = options.nativeError ? { error: options.nativeError } : { result: 'tun-ready' };
      this.emit('message', { message: new TextEncoder().encode(JSON.stringify(result) + 'EOF').buffer });
    }
    async close() { calls.push('channel-close'); if (options.channelClose) await options.channelClose(); }
  }
  const sandbox = {
    exports: {}, console: { debug() {}, log() {}, error() {}, warn() {} },
    setTimeout, clearTimeout, Error, TextEncoder, Uint8Array,
    util: { TextDecoder: class { decodeToString(data) { return new TextDecoder().decode(data); } } },
    socket: { constructLocalSocketInstance() { const s = new LocalSocket(); sockets.push(s); return s; } },
    vpnExtension: { createVpnConnection() {
      calls.push('connection');
      const connection = {
        async create(value) { calls.push('create'); connection.config = value;
          return options.create ? await options.create() : 41; },
        async destroy() { calls.push('destroy'); if (options.destroy) await options.destroy(); },
        async protect(fd) { calls.push('protect'); if (options.protect) await options.protect(fd); }
      };
      connections.push(connection);
      return connection;
    } },
    PlatformNetworkMonitor: class {
      async start() { calls.push('network'); if (options.network) await options.network(); }
      stop() { calls.push('network-stop'); }
    },
    getVpnOptions() { calls.push('config'); if (options.configError) throw new Error(options.configError); return JSON.stringify(config); },
    getTunStartToken() { calls.push('token'); return '17'; },
    stopTun() { calls.push('native-stop'); return options.stopNative ? options.stopNative() : ''; },
    startListener() { calls.push('listeners'); return options.listeners !== false; },
    setFdMap(id) { calls.push('protect-ack:' + id); },
    ClashRpcType: { startClash: 13, stopClash: 14 },
    ProxyMode: { Global: 'global', Rule: 'rule' }
  };
  vm.createContext(sandbox);
  vm.runInContext([code(policy), ...sources.map(code)].join('\n') +
    '\nexports.FlClashVpnService = FlClashVpnService; exports.VpnRunOwner = VpnRunOwner;', sandbox);
  const service = new sandbox.exports.FlClashVpnService({ filesDir: '/test' });
  if (options.timeoutMs) service.lifecycle.timeoutMs = options.timeoutMs;
  return { service, calls, connections, sockets, config, options, Owner: sandbox.exports.VpnRunOwner };
}

test('token precedes the first await; listeners wait for actual native ready; repeated starts share one operation', async () => {
  const network = deferred(), native = deferred();
  const f = fixture({ network: () => network.promise, native: () => native.promise });
  const first = f.service.startVpn();
  assert.strictEqual(f.service.startVpn(), first);
  assert.deepEqual(f.calls, ['config', 'token', 'network']);
  network.resolve();
  await until(() => f.calls.includes('native-start'));
  assert.equal(f.calls.includes('listeners'), false);
  assert.deepEqual(f.sockets[0].request.params, [41, '17']);
  native.resolve();
  assert.equal((await first).state, 'Running');
  assert.equal((await f.service.startVpn()).state, 'Running');
  assert.equal(f.calls.filter(x => x === 'create').length, 1);
  assert.equal((await f.service.stopVpn()).state, 'Stopped');
  assert.ok(f.calls.indexOf('native-stop') < f.calls.indexOf('channel-close'));
});

for (const [name, option, stage, hasConnection] of [
  ['validation', { configError: 'bad config' }, 'prepare', false],
  ['network', { network: async () => { throw new Error('offline'); } }, 'prepare', false],
  ['create', { create: async () => { throw new Error('create failed'); } }, 'system-create', true],
  ['invalid fd', { create: async () => -1 }, 'system-create', true],
  ['native', { nativeError: 'native failed' }, 'native-start', true],
  ['listener', { listeners: false }, 'listeners-start', true]
]) {
  test(name + ' failure reports its stage and cleans only acquired resources', async () => {
    const f = fixture(option);
    const result = await f.service.startVpn();
    assert.equal(result.state, 'Stopped');
    assert.equal(result.stage, stage);
    assert.ok(result.error);
    assert.equal(result.cleanupErrors.length, 0);
    assert.equal(f.calls.includes('destroy'), hasConnection);
    assert.equal(f.service.vpnConnection, undefined);
  });
}

test('stop during network preparation cancels before create and coalesces repeated stop', async () => {
  const network = deferred();
  const f = fixture({ network: () => network.promise });
  const start = f.service.startVpn(), stop = f.service.stopVpn();
  assert.strictEqual(f.service.stopVpn(), stop);
  assert.equal(f.calls.includes('native-stop'), true);
  assert.equal((await f.service.startVpn()).stage, 'start-blocked');
  network.resolve();
  assert.equal((await stop).state, 'Stopped');
  assert.equal((await start).stage, 'start-cancelled');
  assert.equal(f.calls.includes('create'), false);
});

test('stop waits for pending create and destroy; late fd never reaches native startup', async () => {
  const create = deferred(), destroy = deferred();
  const f = fixture({ create: () => create.promise, destroy: () => destroy.promise });
  const start = f.service.startVpn();
  await until(() => f.calls.includes('create'));
  const stop = f.service.stopVpn();
  let stopped = false; stop.then(() => { stopped = true; });
  assert.equal(f.calls.includes('destroy'), false);
  assert.equal(f.service.vpnConnection, f.connections[0]);
  create.resolve(51);
  await until(() => f.calls.includes('destroy'));
  assert.equal(stopped, false);
  assert.equal(f.calls.includes('native-start'), false);
  assert.equal(f.service.vpnConnection, f.connections[0]);
  destroy.resolve();
  assert.equal((await stop).state, 'Stopped');
  assert.equal((await start).state, 'Stopped');
  assert.equal(f.service.vpnConnection, undefined);
});

test('stop while waiting native ready rejects that waiter and does not wait for a new ACK', async () => {
  const native = deferred();
  const f = fixture({ native: () => native.promise });
  const start = f.service.startVpn();
  await until(() => f.calls.includes('native-start'));
  assert.equal((await f.service.stopVpn()).state, 'Stopped');
  const result = await start;
  assert.equal(result.state, 'Stopped');
  assert.equal(result.stage, 'start-cancelled');
  native.resolve();
  await setImmediate();
  assert.equal(f.calls.includes('listeners'), false);
});

test('destroy failure is retained and retried, never cleared by failed Start cleanup continuation', async () => {
  const create = deferred();
  let fail = true;
  const f = fixture({ create: () => create.promise, destroy: async () => { if (fail) throw new Error('SDK refused'); } });
  const start = f.service.startVpn();
  await until(() => f.calls.includes('create'));
  const stop = f.service.stopVpn(); create.resolve(41);
  const failed = await stop;
  assert.equal(failed.state, 'CleanupFailed');
  assert.match(failed.cleanupErrors[0], /SDK refused/);
  assert.equal((await start).state, 'CleanupFailed');
  assert.equal(f.calls.filter(x => x === 'destroy').length, 1);
  assert.equal(f.service.vpnConnection, f.connections[0]);
  assert.equal((await f.service.startVpn()).stage, 'start-blocked');
  fail = false;
  assert.equal((await f.service.stopVpn()).state, 'Stopped');
  assert.equal(f.service.vpnConnection, undefined);
});

test('native cleanup error keeps run retryable even after system destroy succeeds', async () => {
  let error = 'TUN close failed';
  const f = fixture({ stopNative: () => error });
  await f.service.startVpn();
  const stopped = await f.service.stopVpn();
  assert.equal(stopped.state, 'CleanupFailed');
  assert.match(stopped.cleanupErrors[0], /TUN close failed/);
  assert.equal(f.service.vpnConnection, undefined);
  assert.equal((await f.service.startVpn()).stage, 'start-blocked');
  error = '';
  assert.equal((await f.service.stopVpn()).state, 'Stopped');
  assert.equal(f.calls.filter(x => x === 'destroy').length, 1);
});

test('failed startup preserves both the initiating error and rollback error', async () => {
  const f = fixture({ nativeError: 'native setup failed', destroy: async () => { throw new Error('destroy failed'); } });
  const result = await f.service.startVpn();
  assert.equal(result.state, 'CleanupFailed');
  assert.equal(result.stage, 'native-start');
  assert.equal(result.error, 'native setup failed');
  assert.match(result.cleanupErrors[0], /destroy failed/);
  assert.equal(f.service.vpnConnection, f.connections[0]);
  f.options.destroy = undefined;
  assert.equal((await f.service.stopVpn()).state, 'Stopped');
});

test('native stop exception is a cleanup result and does not skip system destruction', async () => {
  const f = fixture({ stopNative: () => { throw new Error('NAPI failure'); } });
  await f.service.startVpn();
  const result = await f.service.stopVpn();
  assert.equal(result.state, 'CleanupFailed');
  assert.match(result.cleanupErrors[0], /NAPI failure/);
  assert.equal(f.calls.includes('destroy'), true);
  f.options.stopNative = undefined;
  await f.service.stopVpn();
});

test('channel close failure retains its socket for retry', async () => {
  let failed = true;
  const f = fixture({ channelClose: async () => { if (failed) throw new Error('socket close failed'); } });
  await f.service.startVpn();
  assert.equal((await f.service.stopVpn()).state, 'CleanupFailed');
  assert.equal(f.service.clashSocket, f.sockets[0]);
  failed = false;
  assert.equal((await f.service.stopVpn()).state, 'Stopped');
  assert.equal(f.service.clashSocket, undefined);
});

test('destroy timeout preserves the pending SDK operation and does not issue duplicate destroy', async () => {
  const destroy = deferred();
  const f = fixture({ timeoutMs: 10, destroy: () => destroy.promise });
  await f.service.startVpn();
  const result = await f.service.stopVpn();
  assert.equal(result.state, 'CleanupFailed');
  assert.match(result.cleanupErrors[0], /结果未知/);
  assert.equal(f.service.vpnConnection, f.connections[0]);
  const retry = f.service.stopVpn();
  assert.equal((await f.service.startVpn()).stage, 'start-blocked');
  destroy.resolve();
  assert.equal((await retry).state, 'Stopped');
  assert.equal(f.calls.filter(x => x === 'destroy').length, 1);
});

test('create timeout keeps ownership and cleans a late result without another user request', async () => {
  const create = deferred();
  const f = fixture({ timeoutMs: 10, create: () => create.promise });
  const start = f.service.startVpn();
  const result = await start;
  assert.equal(result.state, 'CleanupFailed');
  assert.equal(f.service.vpnConnection, f.connections[0]);
  assert.equal((await f.service.startVpn()).stage, 'start-blocked');
  create.resolve(41);
  await until(() => f.service.lifecycle.snapshot().state === 'Stopped');
  assert.equal(f.calls.filter(x => x === 'destroy').length, 1);
  assert.equal(f.calls.includes('native-start'), false);
});

test('second run starts only after previous cleanup; stale socket callbacks cannot stop it', async () => {
  const f = fixture();
  const first = await f.service.startVpn();
  const oldClose = f.sockets[0].events.get('close');
  await f.service.stopVpn();
  const second = await f.service.startVpn();
  assert.ok(second.generation > first.generation);
  oldClose();
  await setImmediate();
  assert.equal(f.service.lifecycle.snapshot().state, 'Running');
  assert.equal(f.service.vpnConnection, f.connections[1]);
  await f.service.stopVpn();
});

test('pending SDK protect settles before destroy and cannot acknowledge a cancelled run', async () => {
  const protect = deferred();
  const f = fixture({ protect: () => protect.promise });
  await f.service.startVpn();
  f.service.handleProtectMessage(JSON.stringify({ result: JSON.stringify({ id: 9, value: 65 }) }), f.sockets[0]);
  await until(() => f.calls.includes('protect'));
  const stop = f.service.stopVpn();
  await setImmediate();
  assert.equal(f.calls.includes('destroy'), false);
  assert.equal((await f.service.startVpn()).stage, 'start-blocked');
  protect.resolve();
  assert.equal((await stop).state, 'Stopped');
  assert.equal(f.calls.includes('protect-ack:9'), false);
  assert.equal((await f.service.startVpn()).state, 'Running');
  await f.service.stopVpn();
});

test('protect completing while channel close is pending cannot ACK a cancelled owner', async () => {
  const protect = deferred(), close = deferred();
  const f = fixture({ protect: () => protect.promise, channelClose: () => close.promise });
  await f.service.startVpn();
  f.service.handleProtectMessage(JSON.stringify({ result: JSON.stringify({ id: 10, value: 65 }) }), f.sockets[0]);
  await until(() => f.calls.includes('protect'));
  const stop = f.service.stopVpn();
  protect.resolve();
  await setImmediate();
  assert.equal(f.calls.includes('protect-ack:10'), false);
  close.resolve();
  assert.equal((await stop).state, 'Stopped');
});

test('RPC Stop received before an older Start fences that command, same sequence is idempotent', async () => {
  const f = fixture();
  const stop = JSON.parse(await f.service.onRemoteMessage(14, ['client-one', 2]));
  assert.equal(stop.state, 'Stopped');
  const late = JSON.parse(await f.service.onRemoteMessage(13, ['client-one', 1]));
  assert.equal(late.stage, 'stale-command');
  assert.equal(late.generation, stop.generation);
  assert.equal(f.calls.length, 0);
  const first = f.service.onRemoteMessage(13, ['client-one', 3]);
  const duplicate = f.service.onRemoteMessage(13, ['client-one', 3]);
  assert.equal(await first, await duplicate);
  assert.equal(f.calls.filter(x => x === 'create').length, 1);
  await f.service.stopVpn();
});

test('CommonVpn rejects empty allowlist and wrong-owner destroy without mutating the active object', async () => {
  const f = fixture();
  f.config.accessControl.acceptList = [];
  assert.equal((await f.service.startVpn()).state, 'Stopped');
  assert.equal(f.calls.includes('connection'), false);
  f.config.accessControl.acceptList = ['com.example.selected'];
  await f.service.startVpn();
  await assert.rejects(f.service.destroyVpn(new f.Owner(99)), /其他运行实例/);
  assert.equal(f.service.vpnConnection, f.connections[0]);
  await f.service.stopVpn();
});
