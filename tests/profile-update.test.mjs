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
const importSource = readFileSync(new URL('../proxy_core/src/main/ets/ProfileImport.ets', import.meta.url), 'utf8');
const importCode = stripTypeScriptTypes(importSource.replace(/^import .*\r?\n/gm, '').replace(/^export /gm, ''), { mode: 'transform' });
function deferred() {
  let resolve;
  const promise = new Promise(yes => { resolve = yes; });
  return { promise, resolve };
}
const body = name => '# ' + name + '\r\n' + 'rules: [MATCH,DIRECT]\r\n# ' + 'original'.repeat(40);
async function fixture(t) {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'clashbox-profile-'));
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const handles = new Map(), names = new Map(), hooks = {};
  async function profileDirectory(_context, id) {
    const dir = path.join(directory, id);
    await fs.mkdir(dir, { recursive: true });
    return dir;
  }
  const file = {
    OpenMode: { CREATE: 1, READ_WRITE: 2, TRUNC: 4, WRITE_ONLY: 8, READ_ONLY: 16 }, AccessModeType: { EXIST: 0 },
    async open(name, mode) { const handle = await fs.open(name, mode === 16 ? 'r' : 'w+'); handles.set(handle.fd, handle); names.set(handle.fd, name); return { fd: handle.fd }; },
    async write(fd, bytes) {
      await hooks.beforeWrite?.(names.get(fd));
      return (await handles.get(fd).write(Buffer.from(bytes))).bytesWritten;
    },
    async read(fd, bytes) { return (await handles.get(fd).read(new Uint8Array(bytes))).bytesRead; },
    async stat(fd) { return handles.get(fd).stat(); },
    async close(fd) { const handle = handles.get(fd); handles.delete(fd); names.delete(fd); await handle.close(); },
    async fsync(fd) { await handles.get(fd).sync(); },
    async access(name) { try { await fs.access(name); return true; } catch { return false; } },
    readText: name => fs.readFile(name, 'utf8'), unlink: name => fs.unlink(name),
    renameSync(from, to) { hooks.beforeRename?.(from, to); renameSync(from, to); },
    listFile: name => fs.readdir(name), rmdir: name => fs.rm(name, { recursive: true }),
    mkdir: (name, recursive = false) => fs.mkdir(name, { recursive })
  };
  const sandbox = { fs: file, JSON, Map, Math, Date, Error, ArrayBuffer, Uint8Array, URL,
    console: { debug() {}, info() {}, warn() {}, error() {} },
    setTimeout: callback => queueMicrotask(callback),
    util: {
      TextEncoder: class { encodeInto(value) { return new TextEncoder().encode(value); } },
      TextDecoder: { create: (encoding, options) => ({ decodeToString: bytes => new TextDecoder(encoding, options).decode(bytes) }) },
      Base64Helper: class { decodeSync(value) { return Uint8Array.from(Buffer.from(value, 'base64')); } }
    },
    getProfilePath: async (context, id) => path.join(await profileDirectory(context, id), 'config.yaml'),
    getProfileDir: profileDirectory,
    getProfilesPath: async () => directory,
    YamlUtils: { convertUniversalToClashYaml() { return null; } },
    SubscriptionInfo: { formHString: value => value }
  };
  vm.createContext(sandbox);
  vm.runInContext(importCode + '\n' + code + '\nglobalThis.Profile = Profile;', sandbox);
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
  return { directory, make, rpc, hooks, read: id => fs.readFile(path.join(directory, id, 'config.yaml'), 'utf8') };
}

test('native subscription failure remains visible and never invokes unverified NetworkKit fallback', async t => {
  const f = await fixture(t), p = f.make('native');
  await p.save('previous', async () => '');
  let requests = 0;
  const destinations = [];
  f.rpc.downloadConfig = async (_url, _ua, target) => { requests++; destinations.push(target); throw new Error('management network is transitioning'); };
  await assert.rejects(p.update(f.rpc), /订阅下载失败: management network is transitioning/);
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
  assert.deepEqual((await fs.readdir(f.directory)).sort(), ['a', 'b']);
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
  const rejected = assert.rejects(old, /旧更新已取消/);
  await entered.promise;
  await p.update(f.rpc);
  gate.resolve(); await rejected;
  assert.equal(await f.read('same'), body('2'));
  assert.equal(p.name, '2.yaml');
});

test('a manual save during subscription validation wins over its delayed successful validation', async t => {
  const f = await fixture(t), p = f.make('edit'), gate = deferred(), entered = deferred();
  f.rpc.vailConfig = async () => { entered.resolve(); await gate.promise; return ''; };
  const old = p.update(f.rpc), rejected = assert.rejects(old, /旧更新已取消/);
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
    const old = p.update(f.rpc), rejected = assert.rejects(old, /旧更新已取消/);
    await entered.promise;
    if (change === 'url') p.url = 'https://example.test/replacement';
    else await p.delete();
    gate.resolve(); await rejected;
    if (change === 'url') assert.equal(await f.read(change), 'previous');
    else await assert.rejects(f.read(change), { code: 'ENOENT' });
    assert.equal(p.name, null);
  });
}

async function bundleFile(f, name, config = body(name)) {
  const filename = path.join(f.directory, name + '.bundle');
  await fs.writeFile(filename, JSON.stringify({
    format: 'clashbox-profile-bundle-v1', configBase64: Buffer.from(config).toString('base64'),
    resources: [{ path: 'rules/local.yaml', base64: Buffer.from('payload: [' + name + ']\n').toString('base64') }]
  }));
  return filename;
}

test('URI import swaps a complete decoded resource bundle and preserves the previous directory', async t => {
  const f = await fixture(t), p = f.make('bundle');
  await p.save('previous config', async () => '');
  const input = await bundleFile(f, 'incoming');
  await p.saveByUri(input);
  assert.equal(await f.read('bundle'), body('incoming'));
  assert.equal(await fs.readFile(path.join(f.directory, 'bundle/rules/local.yaml'), 'utf8'), 'payload: [incoming]\n');
  const previous = (await fs.readdir(f.directory)).filter(name => name.startsWith('bundle.previous-'));
  assert.equal(previous.length, 1);
  assert.equal(await fs.readFile(path.join(f.directory, previous[0], 'config.yaml'), 'utf8'), 'previous config');
});

for (const change of ['save', 'update', 'delete', 'import', 'target-id']) {
  test('a newer ' + change + ' supersedes an older URI import while its staging write is pending', async t => {
    const f = await fixture(t), p = f.make('owned'), entered = deferred(), release = deferred();
    await p.save('previous', async () => '');
    const input = await bundleFile(f, 'old-import');
    let delayed = false;
    f.hooks.beforeWrite = async name => {
      if (!delayed && name.includes('.import-')) { delayed = true; entered.resolve(); await release.promise; }
    };
    const old = p.saveByUri(input), rejected = assert.rejects(old, /旧更新已取消/);
    await entered.promise;
    let expected;
    if (change === 'save') { expected = 'new manual'; await p.save(expected, async () => ''); }
    else if (change === 'update') { expected = body(p.url); await p.update(f.rpc); }
    else if (change === 'delete') await p.delete();
    else if (change === 'import') { expected = body('new-import'); await p.saveByUri(await bundleFile(f, 'new-import')); }
    else { expected = 'previous'; p.id = 'replacement-target'; }
    release.resolve(); await rejected;
    if (change === 'delete') await assert.rejects(f.read('owned'), { code: 'ENOENT' });
    else assert.equal(await f.read('owned'), expected);
    if (change === 'target-id') await assert.rejects(f.read('replacement-target'), { code: 'ENOENT' });
    assert.equal((await fs.readdir(f.directory)).some(name => name.startsWith('owned.import-')), false);
  });
}

test('failed import publication rolls back synchronously before a queued newer edit can run', async t => {
  const f = await fixture(t), p = f.make('rollback'), edited = deferred(), renames = [];
  await p.save('previous', async () => '');
  const input = await bundleFile(f, 'failed-import');
  const target = path.join(f.directory, 'rollback');
  f.hooks.beforeRename = (from, to) => {
    if (from === target) {
      renames.push('backup');
      queueMicrotask(() => p.save('new edit', async () => '').then(() => edited.resolve()));
    } else if (from.includes('.import-')) {
      renames.push('publish-failed');
      throw new Error('publication failed');
    } else if (from.includes('.previous-')) renames.push('rollback');
  };
  await assert.rejects(p.saveByUri(input), /publication failed/);
  await edited.promise;
  assert.deepEqual(renames, ['backup', 'publish-failed', 'rollback']);
  assert.equal(await f.read('rollback'), 'new edit');
  assert.equal((await fs.readdir(f.directory)).some(name => name.startsWith('rollback.import-')), false);
});
