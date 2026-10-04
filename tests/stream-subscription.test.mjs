import assert from 'node:assert/strict';
import test from 'node:test';
import { StreamFrames, StreamSubscription } from '../proxy_core/src/main/ets/rpc/StreamSubscription.ts';

class TestTimers {
  tasks = new Map();
  next = 1;
  set(fn, delay) { const id = this.next++; this.tasks.set(id, { fn, delay }); return id; }
  clear(id) { this.tasks.delete(id); }
  runNext() {
    const [id, task] = [...this.tasks].sort((a, b) => a[1].delay - b[1].delay)[0] ?? [];
    assert.ok(task, 'expected a scheduled retry or ready deadline');
    this.tasks.delete(id); task.fn(); return task.delay;
  }
}

class TestSocket {
  decoder = new TextDecoder();
  closed = false;
  connect(onText, onDisconnect) { this.onText = onText; this.onDisconnect = onDisconnect; return Promise.resolve(); }
  close() { this.closed = true; }
  text(value) { this.onText(value); }
  bytes(value) { this.onText(this.decoder.decode(value, { stream: true })); }
  disconnect(reason = 'socket closed') { this.onDisconnect(reason); }
}

function fixture(lines = true) {
  const sockets = [], messages = [], warnings = [];
  const timers = new TestTimers();
  const subscription = new StreamSubscription(lines, () => {
    const socket = new TestSocket(); sockets.push(socket); return socket;
  }, message => messages.push(message), warning => warnings.push(warning), timers);
  return { subscription, sockets, messages, warnings, timers };
}

test('JSON lines retain half frames and multiple coalesced frames', () => {
  const frames = new StreamFrames(true);
  assert.deepEqual(frames.push('{"rea'), []);
  assert.deepEqual(frames.push('dy":true}\n{"result":"EO'), ['{"ready":true}']);
  assert.deepEqual(frames.push('F"}\n{"result":"next"}\n'), ['{"result":"EOF"}', '{"result":"next"}']);
});

test('legacy EOF boundary is accepted only after a complete JSON frame', () => {
  const frames = new StreamFrames(false);
  const first = JSON.stringify({ result: JSON.stringify({ name: 'beforeEOFafter', text: '日志' }) });
  const second = JSON.stringify({ result: 'nextEOFvalue' });
  const wire = first + 'EOF' + second + 'EOF';
  const received = [];
  for (const character of wire) received.push(...frames.push(character));
  assert.deepEqual(received, [first, second]);
});

test('per-socket UTF-8 decoding preserves split multibyte characters and EOF payloads', async () => {
  const f = fixture();
  const registration = f.subscription.start();
  const payload = JSON.stringify({ type: 'delay', data: { name: '香港🚀EOF节点', value: 8 } });
  const wire = new TextEncoder().encode('{"ready":true}\n' + JSON.stringify({ result: payload }) + '\n');
  for (const byte of wire) f.sockets[0].bytes(new Uint8Array([byte]));
  const cancel = await registration;
  assert.deepEqual(f.messages, [payload]);
  cancel();
});

test('event registration resolves only after a complete server ready frame', async () => {
  const f = fixture();
  let registered = false;
  const registration = f.subscription.start().then(cancel => { registered = true; return cancel; });
  await Promise.resolve();
  assert.equal(registered, false);
  f.sockets[0].text('{"ready":tr');
  await Promise.resolve();
  assert.equal(registered, false);
  f.sockets[0].text('ue}\n');
  const cancel = await registration;
  assert.equal(registered, true);
  assert.equal(f.timers.tasks.size, 0);
  cancel();
});

test('cancel removes retries and ignores stale socket data/error callbacks', async () => {
  const f = fixture();
  const registration = f.subscription.start();
  f.sockets[0].text('{"ready":true}\n');
  const cancel = await registration;
  f.sockets[0].disconnect();
  assert.equal(f.timers.tasks.size, 1);
  cancel(); cancel();
  assert.equal(f.timers.tasks.size, 0);
  f.sockets[0].text(JSON.stringify({ result: 'late' }) + '\n');
  f.sockets[0].disconnect('late error');
  assert.deepEqual(f.messages, []);
  assert.equal(f.sockets.length, 1);
});

test('cancelling a pending ready handshake rejects registration and stops timers', async () => {
  const f = fixture();
  const registration = f.subscription.start();
  f.subscription.cancel();
  await assert.rejects(registration, /cancelled/);
  assert.equal(f.sockets[0].closed, true);
  assert.equal(f.timers.tasks.size, 0);
});

test('disconnect reconnects with a fresh parser and sends resync only after ready', async () => {
  const f = fixture();
  const registration = f.subscription.start();
  f.sockets[0].text('{"ready":true}\n');
  const cancel = await registration;
  f.sockets[0].text('{"result":"abandoned half-frame');
  f.sockets[0].disconnect();
  f.sockets[0].disconnect('duplicate close');
  assert.equal(f.timers.tasks.size, 1);
  assert.equal(f.timers.runNext(), 250);
  assert.equal(f.sockets.length, 2);
  assert.deepEqual(f.messages, []);
  f.sockets[0].text('late"}\n');
  f.sockets[1].text('{"ready":true}\n{"result":"new event"}\n');
  assert.deepEqual(f.messages, [JSON.stringify({ type: 'resync', data: null }), 'new event']);
  assert.match(f.warnings[0], /disconnected/);
  cancel();
});

test('reconnection has a finite budget and explicit terminal notification', async () => {
  const f = fixture();
  const registration = f.subscription.start();
  f.sockets[0].text('{"ready":true}\n');
  await registration;
  const delays = [];
  for (let retry = 0; retry < 5; retry++) {
    f.sockets.at(-1).disconnect('unavailable');
    delays.push(f.timers.runNext());
  }
  f.sockets.at(-1).disconnect('unavailable');
  assert.deepEqual(delays, [250, 500, 1000, 2000, 4000]);
  assert.equal(f.sockets.length, 6);
  assert.equal(f.timers.tasks.size, 0);
  const terminal = JSON.parse(f.messages.at(-1));
  assert.equal(terminal.type, 'subscription-error');
  assert.match(terminal.data, /5 reconnection attempts/);
  assert.match(f.warnings.at(-1), /stopped/);
});

test('cancelling from the recovery observer stops already buffered events', async () => {
  const sockets = [], messages = [];
  const timers = new TestTimers();
  const subscription = new StreamSubscription(true, () => {
    const socket = new TestSocket(); sockets.push(socket); return socket;
  }, message => { messages.push(message); subscription.cancel(); }, () => {}, timers);
  const registration = subscription.start();
  sockets[0].text('{"ready":true}\n');
  await registration;
  sockets[0].disconnect(); timers.runNext();
  sockets[1].text('{"ready":true}\n{"result":"must not arrive"}\n');
  assert.deepEqual(messages, [JSON.stringify({ type: 'resync', data: null })]);
  assert.equal(timers.tasks.size, 0);
});

test('missing initial ready eventually rejects rather than leaving a pending promise', async () => {
  const f = fixture();
  const registration = f.subscription.start();
  const rejected = assert.rejects(registration, /ready timed out/);
  for (let attempt = 0; attempt < 6; attempt++) {
    assert.equal(f.timers.runNext(), 5000);
    if (attempt < 5) f.timers.runNext();
  }
  await rejected;
  assert.equal(f.timers.tasks.size, 0);
  assert.equal(JSON.parse(f.messages.at(-1)).type, 'subscription-error');
});

test('legacy logs complete registration after send and preserve fragmented records', async () => {
  const f = fixture(false);
  const cancel = await f.subscription.start();
  f.sockets[0].text('{"result":"logEOF');
  f.sockets[0].text('text"}EO');
  assert.deepEqual(f.messages, []);
  f.sockets[0].text('F');
  assert.deepEqual(f.messages, ['logEOFtext']);
  cancel();
});

test('malformed protocol and oversized partial frames are bounded failures', async () => {
  assert.throws(() => new StreamFrames(true).push('x'.repeat(1024 * 1024 + 1)), /exceeds/);
  const f = fixture();
  const registration = f.subscription.start();
  f.sockets[0].text('{"ready":true}\n');
  const cancel = await registration;
  f.sockets[0].text('not-json\n');
  assert.equal(f.sockets[0].closed, true);
  assert.equal(f.timers.tasks.size, 1);
  cancel();
});
