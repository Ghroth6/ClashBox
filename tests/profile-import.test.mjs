import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import vm from 'node:vm';
import test from 'node:test';

const source = readFileSync(new URL('../proxy_core/src/main/ets/ProfileImport.ets', import.meta.url), 'utf8');
const code = stripTypeScriptTypes(source.replace(/^import .*;\r?\n/gm, '').replace(/^export /gm, ''));
const util = {
  TextDecoder: { create: (encoding, options) => ({ decodeToString: bytes => new TextDecoder(encoding, options).decode(bytes) }) },
  Base64Helper: class { decodeSync(value) { return Uint8Array.from(Buffer.from(value, 'base64')); } },
};
const { decodeProfileImport } = vm.runInNewContext(code + '\n({ decodeProfileImport })', { util, JSON, Uint8Array, Error });
const input = text => Uint8Array.from(Buffer.from(text));
const rawConfig = '# untouched\r\nmode: rule\r\nrules: [MATCH,DIRECT]\r\n';
const resource = path => ({ path, base64: Buffer.from('payload: []\n').toString('base64') });
const bundle = () => ({ format: 'clashbox-profile-bundle-v1', configBase64: Buffer.from(rawConfig).toString('base64'), resources: [resource('rules/local.yaml')] });

for (const [name, text] of [
  ['flow mapping', "{mixed-port: 7890, mode: rule, proxies: [], rules: ['MATCH,DIRECT']}"],
  ['flow mapping with quoted keys and YAML values', '{"dns": {enable: true}, "mode": rule, "rules": ["MATCH,DIRECT"]}'],
  ['flow mapping with comments and BOM', '\ufeff{ # original comment\r\n mode: rule, rules: ["MATCH,DIRECT"]\r\n}\r\n'],
  ['ordinary JSON', '{"dns":{"enable":true},"rules":["MATCH,DIRECT"],"x-future":"untouched"}\r\n'],
  ['block YAML', rawConfig],
]) {
  test(`${name} is retained byte-for-byte instead of being interpreted as a bundle`, () => {
    const bytes = input(text), files = decodeProfileImport(bytes);
    assert.equal(files.length, 1);
    assert.equal(files[0].path, 'config.yaml');
    assert.deepEqual(Buffer.from(files[0].bytes), Buffer.from(bytes));
  });
}

test('configuration syntax validation is not performed by the bundle discriminator', () => {
  const bytes = input('{bad');
  assert.deepEqual(Buffer.from(decodeProfileImport(bytes)[0].bytes), Buffer.from(bytes));
});

test('explicit versioned JSON bundle still decodes config and resources', () => {
  const files = decodeProfileImport(input(JSON.stringify(bundle())));
  assert.equal(files.length, 2);
  assert.equal(files[0].path, 'config.yaml');
  assert.deepEqual(Buffer.from(files[0].bytes), Buffer.from(rawConfig));
  assert.equal(files[1].path, 'rules/local.yaml');
  assert.equal(Buffer.from(files[1].bytes).toString(), 'payload: []\n');
});

for (const [name, change] of [
  ['missing config', b => { delete b.configBase64; }],
  ['empty config', b => { b.configBase64 = ''; }],
  ['missing resources', b => { delete b.resources; }],
  ['non-array resources', b => { b.resources = {}; }],
  ['too many resources', b => { b.resources = Array.from({ length: 257 }, (_, i) => resource(`rules/${i}.yaml`)); }],
  ['duplicate path', b => { b.resources.push(resource('RULES/local.yaml')); }],
  ['file-directory conflict', b => { b.resources.push(resource('rules/local.yaml/nested')); }],
  ['non-string payload', b => { b.resources[0].base64 = 1; }],
]) {
  test(`identified bundle rejects ${name} without falling back to raw configuration`, () => {
    const value = bundle(); change(value);
    assert.throws(() => decodeProfileImport(input(JSON.stringify(value))));
  });
}

for (const path of ['../outside', '/absolute', 'C:/outside', 'a\\b', 'a//b', './a', 'config.yaml', 'CONFIG.YAML']) {
  test(`identified bundle continues to reject resource path ${path}`, () => {
    const value = bundle(); value.resources = [resource(path)];
    assert.throws(() => decodeProfileImport(input(JSON.stringify(value))), /路径/);
  });
}

test('empty, invalid UTF-8 and oversized input still fail before import', () => {
  assert.throws(() => decodeProfileImport(new Uint8Array()));
  assert.throws(() => decodeProfileImport(new Uint8Array([0xff])));
  assert.throws(() => decodeProfileImport(new Uint8Array(32 * 1024 * 1024 + 1)));
});
