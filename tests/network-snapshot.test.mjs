import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import { setImmediate } from 'node:timers/promises';
import vm from 'node:vm';
import test from 'node:test';

const source = new URL('../proxy_core/src/main/ets/rpc/NetworkSnapshot.ets', import.meta.url);
const input = readFileSync(source, 'utf8');
assert.equal(input, readFileSync(new URL('../proxy_core/src/main/ets/rpc/NetworkSnapshot.ts', import.meta.url), 'utf8'));

function load() {
  const sandbox = {
    connection: { NetCap: { NET_CAPABILITY_INTERNET: 12 }, NetBearType: { BEARER_VPN: 4 } },
    publishNetworkSnapshot: () => { throw new Error('tests must inject native publication'); },
    console, exports: {},
  };
  vm.createContext(sandbox);
  const code = stripTypeScriptTypes(input.replace(/^import .*;\r?\n/gm, '').replace(/^export /gm, ''), { mode: 'strip' });
  vm.runInContext(code + '\nexports.PlatformNetworkMonitor = PlatformNetworkMonitor; exports.buildNetworkSnapshot = buildNetworkSnapshot;', sandbox);
  return sandbox.exports;
}

function properties(name = 'wlan0') {
  return {
    interfaceName: name, domains: '', mtu: 1420,
    linkAddresses: [
      { address: { address: '192.0.2.9', family: 1 }, prefixLength: 24 },
      { address: { address: '2001:db8::9', family: 2 }, prefixLength: 64 },
    ],
    dnses: [{ address: '192.0.2.53', family: 1, port: 0 }, { address: '2001:db8::53', family: 2, port: 5353 }],
    routes: [
      { interface: name, destination: { address: { address: '0.0.0.0', family: 1 }, prefixLength: 0 }, gateway: { address: '192.0.2.1', family: 1 }, hasGateway: true, isDefaultRoute: true },
      { interface: name, destination: { address: { address: '2001:db8::', family: 2 }, prefixLength: 64 }, gateway: { address: '::', family: 2 }, hasGateway: false, isDefaultRoute: false },
    ],
  };
}

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

class Listener {
  events = new Map();
  registered = false;
  unregisterCalls = 0;
  registration;
  register(callback) {
    if (this.registration) { this.registration(callback); return; }
    this.registered = true;
    callback(undefined);
  }
  on(name, callback) {
    assert.equal(this.registered, true, 'register must finish before subscriptions');
    this.events.set(name, callback);
  }
  unregister(callback) { this.unregisterCalls++; this.registered = false; callback(undefined); }
  emit(name) { this.events.get(name)?.({ netId: 11 }); }
}

function fixture() {
  const state = {
    publications: [], logs: [], listeners: [], netId: 11, value: properties(),
    caps: { networkCap: [12], bearerTypes: [1] },
  };
  const dependencies = {
    createConnection() { const listener = new Listener(); state.listeners.push(listener); return listener; },
    async getDefaultNet() { return { netId: state.netId }; },
    async getAllNets() { return [{ netId: state.netId }]; },
    async getCapabilities() { return state.caps; },
    async getProperties() { return state.value; },
    publish(json) { state.publications.push(JSON.parse(json)); return ''; },
    log(message) { state.logs.push(message); },
  };
  return { state, dependencies };
}

async function until(predicate) {
  for (let attempt = 0; attempt < 100; attempt++) { if (predicate()) return; await setImmediate(); }
  assert.fail('asynchronous condition did not settle');
}

test('complete dual-stack model preserves observed data and does not alias inputs', () => {
  const { buildNetworkSnapshot } = load();
  const input = properties();
  const result = JSON.parse(JSON.stringify(buildNetworkSnapshot(77, input, 5)));
  assert.deepEqual(result, {
    generation: 5, networkId: 77, online: true, dns: ['192.0.2.53:53', '[2001:db8::53]:5353'],
    interfaces: [{ name: 'wlan0', index: 0, mtu: 1420, up: true, addresses: ['192.0.2.9/24', '2001:db8::9/64'], routes: [{ destination: '0.0.0.0/0', gateway: '192.0.2.1' }, { destination: '2001:db8::/64' }] }],
  });
  input.linkAddresses[0].address.address = '198.51.100.8';
  input.routes[0].gateway.address = '198.51.100.1';
  assert.equal(result.interfaces[0].addresses[0], '192.0.2.9/24');
  assert.equal(result.interfaces[0].routes[0].gateway, '192.0.2.1');
});

test('model rejects malformed families/prefixes/ports/routes without inventing defaults', () => {
  const { buildNetworkSnapshot } = load();
  const cases = [
    p => { p.linkAddresses[0].address.family = 4; },
    p => { p.linkAddresses[1].address.family = 1; },
    p => { p.linkAddresses[1].prefixLength = 129; },
    p => { p.linkAddresses[1].address.address = '2001:::9'; },
    p => { p.linkAddresses[0].address.address = '192.0.2.999'; },
    p => { p.linkAddresses = []; },
    p => { p.dnses[0].port = 65536; },
    p => { p.dnses[1].address = 'dns.example'; },
    p => { p.routes[0].interface = 'other'; },
    p => { p.routes[0].gateway.family = 2; },
    p => { p.mtu = undefined; },
  ];
  for (const mutate of cases) { const p = properties(); mutate(p); assert.throws(() => buildNetworkSnapshot(1, p, 1)); }
  const p = properties(); p.dnses = []; p.routes[0].isExcludedRoute = true;
  const result = buildNetworkSnapshot(1, p, 1);
  assert.equal(result.dns.length, 0, 'must not add public DNS');
  assert.equal(result.interfaces[0].routes.length, 1, 'excluded routes must not become inclusion routes');
});

test('start is idempotent, waits for first accepted snapshot, then stop publishes offline', async () => {
  const { PlatformNetworkMonitor } = load();
  const { state, dependencies } = fixture();
  const gate = deferred(); dependencies.getProperties = () => gate.promise;
  const monitor = new PlatformNetworkMonitor(dependencies);
  const first = monitor.start();
  assert.equal(monitor.start(), first);
  await setImmediate(); assert.equal(state.publications.length, 0);
  gate.resolve(properties()); await first;
  assert.equal(state.listeners.length, 1);
  assert.equal(state.publications.length, 1);
  assert.equal(state.publications[0].online, true);
  monitor.stop();
  assert.equal(state.listeners[0].unregisterCalls, 1);
  assert.equal(state.publications.at(-1).online, false);
  assert.ok(state.publications.at(-1).generation > state.publications[0].generation);
});

test('a default-network switch during collection retries instead of mixing observations', async () => {
  const { PlatformNetworkMonitor } = load();
  const { state, dependencies } = fixture();
  const sequence = [11, 22, 22, 22];
  dependencies.getDefaultNet = async () => ({ netId: sequence.shift() });
  dependencies.getProperties = async handle => properties('net' + handle.netId);
  const monitor = new PlatformNetworkMonitor(dependencies);
  await monitor.start();
  assert.equal(state.publications.length, 1);
  assert.equal(state.publications[0].networkId, 22);
  assert.equal(state.publications[0].interfaces.at(-1).name, 'net22');
  monitor.stop();
});

test('change events invalidate an in-flight snapshot and refresh serially', async () => {
  const { PlatformNetworkMonitor } = load();
  const { state, dependencies } = fixture();
  const gate = deferred(); let calls = 0, active = 0, maximum = 0;
  dependencies.getProperties = async () => {
    calls++; active++; maximum = Math.max(maximum, active);
    const result = calls === 1 ? await gate.promise : properties('new0');
    active--; return result;
  };
  const monitor = new PlatformNetworkMonitor(dependencies);
  const starting = monitor.start();
  await until(() => calls === 1);
  state.listeners[0].emit('netConnectionPropertiesChange');
  state.listeners[0].emit('netCapabilitiesChange');
  gate.resolve(properties('old0'));
  await starting;
  assert.equal(maximum, 1);
  assert.equal(calls, 2);
  assert.equal(state.publications.length, 1);
  assert.equal(state.publications[0].interfaces[0].name, 'new0');
  monitor.stop();
});

test('all activated internet networks are included, deduplicated, with default interface last and default DNS only', async () => {
  const { PlatformNetworkMonitor } = load();
  const { state, dependencies } = fixture();
  dependencies.getAllNets = async () => [50, 11, 33, 44, 50, 22, 60].map(netId => ({ netId }));
  dependencies.getCapabilities = async handle => ({ networkCap: handle.netId === 44 ? [] : [12], bearerTypes: handle.netId === 33 ? [4] : [1] });
  const fetched = [];
  dependencies.getProperties = async handle => {
    fetched.push(handle.netId);
    const p = properties(handle.netId === 11 || handle.netId === 60 ? 'wlan0' : 'eth0');
    p.mtu = 1400 + handle.netId;
    p.dnses = [{ address: '192.0.2.' + handle.netId, family: 1 }];
    return p;
  };
  const monitor = new PlatformNetworkMonitor(dependencies); await monitor.start();
  const snapshot = state.publications[0];
  assert.deepEqual(fetched, [22, 50, 60, 11]);
  assert.deepEqual(snapshot.interfaces.map(iface => iface.name), ['eth0', 'wlan0']);
  assert.deepEqual(snapshot.interfaces.map(iface => iface.mtu), [1450, 1411]);
  assert.deepEqual(snapshot.dns, ['192.0.2.11:53']);
  assert.equal(snapshot.interfaces.at(-1).routes[0].destination, '0.0.0.0/0');
  monitor.stop();
});

test('active start refreshes observations and propagates an explicit refresh failure', async () => {
  const { PlatformNetworkMonitor } = load();
  const { state, dependencies } = fixture();
  const monitor = new PlatformNetworkMonitor(dependencies); await monitor.start();
  state.value = properties('new0');
  await monitor.start();
  assert.equal(state.listeners.length, 1);
  assert.equal(state.publications.at(-1).interfaces[0].name, 'new0');
  dependencies.getProperties = async () => { throw new Error('explicit refresh failed'); };
  await assert.rejects(monitor.start(), /explicit refresh failed/);
  assert.equal(state.publications.at(-1).online, false);
  monitor.stop();
});

test('stop and restart cannot resurrect an older pending collection', async () => {
  const { PlatformNetworkMonitor } = load();
  const { state, dependencies } = fixture();
  const gate = deferred(); let calls = 0;
  dependencies.getProperties = () => ++calls === 1 ? gate.promise : Promise.resolve(properties('new0'));
  const monitor = new PlatformNetworkMonitor(dependencies);
  const oldStart = monitor.start();
  const oldRejected = assert.rejects(oldStart, /stopped/);
  await until(() => calls === 1);
  monitor.stop();
  await monitor.start();
  const acceptedGeneration = state.publications.at(-1).generation;
  gate.resolve(properties('obsolete0')); await oldRejected;
  assert.equal(state.publications.at(-1).generation, acceptedGeneration);
  assert.equal(state.publications.at(-1).interfaces[0].name, 'new0');
  state.listeners[0].emit('netLost');
  assert.equal(state.publications.at(-1).online, true, 'disposed events must be ignored');
  monitor.stop();
});

test('late registration after stop is unregistered and never publishes online', async () => {
  const { PlatformNetworkMonitor } = load();
  const { state, dependencies } = fixture();
  let callback;
  dependencies.createConnection = () => {
    const listener = new Listener(); listener.registration = cb => { callback = cb; }; state.listeners.push(listener); return listener;
  };
  const monitor = new PlatformNetworkMonitor(dependencies);
  const starting = monitor.start(); const rejected = assert.rejects(starting, /stopped/);
  monitor.stop();
  state.listeners[0].registered = true; callback(undefined); await rejected;
  assert.equal(state.listeners[0].unregisterCalls, 1);
  assert.equal(state.publications.length, 1);
  assert.equal(state.publications[0].online, false);
});

test('initial registration, collection and native publication failures reject and clean up', async () => {
  for (const mode of ['register', 'collect', 'publish', 'create']) {
    const { PlatformNetworkMonitor } = load();
    const { state, dependencies } = fixture();
    if (mode === 'register') dependencies.createConnection = () => { const listener = new Listener(); listener.registration = cb => cb({ code: 201 }); state.listeners.push(listener); return listener; };
    if (mode === 'collect') dependencies.getProperties = async () => { throw new Error('collection denied'); };
    if (mode === 'publish') dependencies.publish = json => { const value = JSON.parse(json); state.publications.push(value); return value.online ? 'rejected' : ''; };
    if (mode === 'create') dependencies.createConnection = () => { throw new Error('create failed'); };
    const monitor = new PlatformNetworkMonitor(dependencies);
    await assert.rejects(monitor.start());
    assert.equal(state.publications.at(-1).online, false, mode);
    if (mode === 'collect' || mode === 'publish') assert.equal(state.listeners[0].unregisterCalls, 1);
  }
});

test('background failures publish offline and a later event can restore the network', async () => {
  const { PlatformNetworkMonitor } = load();
  const { state, dependencies } = fixture();
  const monitor = new PlatformNetworkMonitor(dependencies); await monitor.start();
  dependencies.getProperties = async () => { throw new Error('transient'); };
  state.listeners[0].emit('netConnectionPropertiesChange');
  await until(() => state.publications.at(-1).online === false);
  assert.ok(state.logs.some(message => message.includes('transient')));
  dependencies.getProperties = async () => properties('restored');
  state.listeners[0].emit('netAvailable');
  await until(() => state.publications.at(-1).online === true);
  assert.equal(state.publications.at(-1).interfaces[0].name, 'restored');
  monitor.stop();
});

test('no default, VPN and non-internet networks remain offline without fetching their properties', async () => {
  for (const mode of ['none', 'vpn', 'local']) {
    const { PlatformNetworkMonitor } = load();
    const { state, dependencies } = fixture();
    if (mode === 'none') state.netId = 0;
    if (mode === 'vpn') state.caps.bearerTypes = [4];
    if (mode === 'local') state.caps.networkCap = [];
    dependencies.getProperties = async () => { assert.fail('must not inspect unusable network'); };
    const monitor = new PlatformNetworkMonitor(dependencies); await monitor.start();
    assert.equal(state.publications[0].online, false); assert.equal(state.publications[0].dns.length, 0);
    monitor.stop();
  }
});

test('netLost immediately publishes offline and invalidates a pending update', async () => {
  const { PlatformNetworkMonitor } = load();
  const { state, dependencies } = fixture();
  const monitor = new PlatformNetworkMonitor(dependencies); await monitor.start();
  const gate = deferred(); let calls = 0;
  dependencies.getProperties = () => { calls++; return gate.promise; };
  state.listeners[0].emit('netConnectionPropertiesChange'); await until(() => calls === 1);
  state.netId = 0; state.listeners[0].emit('netLost');
  assert.equal(state.publications.at(-1).online, false);
  const lostGeneration = state.publications.at(-1).generation;
  gate.resolve(properties('old0'));
  await until(() => state.publications.at(-1).generation > lostGeneration);
  assert.equal(state.publications.at(-1).online, false);
  assert.equal(state.publications.filter(value => value.online).length, 1);
  monitor.stop();
});
