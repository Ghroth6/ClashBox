import assert from 'node:assert/strict';
import { readFileSync, renameSync } from 'node:fs';
import fs from 'node:fs/promises';
import { stripTypeScriptTypes } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import vm from 'node:vm';
import test from 'node:test';

// Execute production Profile update/save/delete against real host files. Only
// HarmonyOS file handles, native RPC and conversion are substituted.
const source = readFileSync(new URL('../proxy_core/src/main/ets/Profile.ets', import.meta.url), 'utf8');
const code = stripTypeScriptTypes(source.replace(/^import .*\r?\n/gm, '').replace(/^export /gm, '')
  .replace(/^@Concurrent\r?\n/gm, ''), { mode: 'transform' });
function deferred() {
  let resolve;
  const promise = new Promise(yes => { resolve = yes; });
  return { promise, resolve };
}
const body = name => '# ' + name + '\r\n' + 'rules: [MATCH,DIRECT]\r\n# ' + 'original'.repeat(40);
async function fixture(t) {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'clashbox-profile-'));
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const handles = new Map();
  const file = {
    OpenMode: { CREATE: 1, READ_WRITE: 2, TRUNC: 4 }, AccessModeType: { EXIST: 0 },
    async open(name) { const handle = await fs.open(name, 'w+'); handles.set(handle.fd, handle); return { fd: handle.fd }; },
    async write(fd, bytes) { return (await handles.get(fd).write(Buffer.from(bytes))).bytesWritten; },
    async close(fd) { const handle = handles.get(fd); handles.delete(fd); await handle.close(); },
    async fsync(fd) { await handles.get(fd).sync(); },
    async access(name) { try { await fs.access(name); return true; } catch { return false; } },
    readText: name => fs.readFile(name, 'utf8'), unlink: name => fs.unlink(name),
    renameSync, listFile: name => fs.readdir(name), rmdir: name => fs.rmdir(name)
  };
  const sandbox = { fs: file, JSON, Map, Math, Date, Error, ArrayBuffer, Uint8Array, URL,
    console: { debug() {}, info() {}, warn() {}, error() {} },
    setTimeout: callback => queueMicrotask(callback),
    util: { TextEncoder: class { encodeInto(value) { return new TextEncoder().encode(value); } } },
    getProfilePath: async (_context, id) => path.join(directory, id + '.yaml'),
    getProfilesPath: async () => directory,
    YamlUtils: { convertUniversalToClashYaml() { return null; } },
    SubscriptionInfo: { formHString: value => value }
  };
  vm.createContext(sandbox);
  vm.runInContext(code + '\nglobalThis.Profile = Profile;', sandbox);
  const make = name => {
    const profile = new sandbox.Profile(undefined, 'https://example.test/' + name);
    profile.id = name; profile.context = { tempDir: directory };
    profile.cleanScriptBackup = () => {};
    return profile;
  };
  const rpc = { async downloadConfig(url, _ua, target) {
    await fs.writeFile(target, body(url));
    return JSON.stringify({ 'content-disposition': 'attachment; filename="fresh.yaml"' });
  }, async vailConfig() { return ''; } };
  return { directory, make, rpc, read: id => fs.readFile(path.join(directory, id + '.yaml'), 'utf8') };
}

test('native subscription failure remains visible and never invokes unverified NetworkKit fallback', async t => {
  const f = await fixture(t), p = f.make('native');
  await p.save('previous', async () => '');
  let requests = 0;
  const destinations = [];
  f.rpc.downloadConfig = async (_url, _ua, target) => { requests++; destinations.push(target); throw new Error('management network is transitioning'); };
  await assert.rejects(p.update(f.rpc), /系统 HTTP 路径尚未验证.*management network is transitioning/);
  assert.equal(requests, 2);
  assert.equal(new Set(destinations).size, 2);
  assert.equal(await f.read('native'), 'previous');
});

test('different profiles download and validate concurrently without sharing temporary files', async t => {
  const f = await fixture(t), a = f.make('a'), b = f.make('b');
  const pending = [], downloads = [];
  f.rpc.vailConfig = async filename => { pending.push(filename); if (pending.length === 1) await new Promise(resolve => setImmediate(resolve)); return ''; };
  const original = f.rpc.downloadConfig;
  f.rpc.downloadConfig = async (...args) => { downloads.push(args[2]); return original(...args); };
  await Promise.all([a.update(f.rpc), b.update(f.rpc)]);
  assert.equal(new Set(downloads).size, 2);
  assert.equal(new Set(pending).size, 2);
  assert.equal(await f.read('a'), body(a.url));
  assert.equal(await f.read('b'), body(b.url));
  assert.deepEqual((await fs.readdir(f.directory)).sort(), ['a.yaml', 'b.yaml']);
});

test('a slow old response cannot replace a newer update or its metadata', async t => {
  const f = await fixture(t), p = f.make('same'), gate = deferred(), entered = deferred();
  let n = 0;
  f.rpc.downloadConfig = async (_url, _ua, target) => {
    const id = ++n;
    if (id === 1) { entered.resolve(); await gate.promise; }
    await fs.writeFile(target, body(String(id)));
    return JSON.stringify({ 'content-disposition': `attachment; filename="${id}.yaml"` });
  };
  const old = p.update(f.rpc);
  const rejected = assert.rejects(old, /旧订阅更新已取消/);
  await entered.promise;
  await p.update(f.rpc);
  gate.resolve(); await rejected;
  assert.equal(await f.read('same'), body('2'));
  assert.equal(p.name, '2.yaml');
});

test('a manual save during subscription validation wins over its delayed successful validation', async t => {
  const f = await fixture(t), p = f.make('edit'), gate = deferred(), entered = deferred();
  f.rpc.vailConfig = async () => { entered.resolve(); await gate.promise; return ''; };
  const old = p.update(f.rpc), rejected = assert.rejects(old, /旧订阅更新已取消/);
  await entered.promise;
  await p.save('manual original\r\n', async () => '');
  gate.resolve(); await rejected;
  assert.equal(await f.read('edit'), 'manual original\r\n');
  assert.equal(p.name, null);
});

for (const change of ['url', 'delete']) {
  test(change + ' invalidates a downloaded subscription before publication', async t => {
    const f = await fixture(t), p = f.make(change), gate = deferred(), entered = deferred();
    await p.save('previous', async () => '');
    f.rpc.vailConfig = async () => { entered.resolve(); await gate.promise; return ''; };
    const old = p.update(f.rpc), rejected = assert.rejects(old, /旧订阅更新已取消/);
    await entered.promise;
    if (change === 'url') p.url = 'https://example.test/replacement';
    else await p.delete();
    gate.resolve(); await rejected;
    if (change === 'url') assert.equal(await f.read(change), 'previous');
    else await assert.rejects(f.read(change), { code: 'ENOENT' });
    assert.equal(p.name, null);
  });
}
