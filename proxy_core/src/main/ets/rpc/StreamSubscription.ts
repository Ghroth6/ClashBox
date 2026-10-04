// Framing and subscription lifetime are independent of the platform socket.
// Each socket supplies text from its own streaming UTF-8 decoder.
export interface StreamSocket {
  connect(onText: (text: string) => void, onDisconnect: (reason: string) => void): Promise<void>;
  close(): void;
}

export interface StreamTimers {
  set(callback: () => void, milliseconds: number): number;
  clear(id: number): void;
}

interface StreamEnvelope {
  ready?: boolean;
  result?: string;
  error?: string;
}

interface StreamControl {
  type: string;
  data: string | null;
}

export class StreamFrames {
  private buffer: string = '';
  private lines: boolean;

  constructor(lines: boolean) { this.lines = lines; }

  push(text: string): string[] {
    this.buffer += text;
    if (this.buffer.length > 1024 * 1024) throw new Error('RPC stream frame exceeds 1 MiB');
    const frames: string[] = [];
    if (this.lines) {
      let end = this.buffer.indexOf('\n');
      while (end >= 0) {
        const frame = this.buffer.slice(0, end).trim();
        this.buffer = this.buffer.slice(end + 1);
        if (frame !== '') { JSON.parse(frame); frames.push(frame); }
        end = this.buffer.indexOf('\n');
      }
    } else {
      // Legacy logs use EOF. It can occur inside JSON strings; only accept a
      // candidate delimiter when the entire preceding frame parses as JSON.
      let end = this.buffer.indexOf('EOF');
      while (end >= 0) {
        const frame = this.buffer.slice(0, end);
        let complete = false;
        try { JSON.parse(frame); complete = true; } catch (_) {}
        if (complete) {
          frames.push(frame);
          this.buffer = this.buffer.slice(end + 3);
          end = this.buffer.indexOf('EOF');
        } else {
          end = this.buffer.indexOf('EOF', end + 3);
        }
      }
    }
    return frames;
  }
}

// At most five reconnections during one subscription, with bounded backoff.
// A newly connected event socket is not usable until its server ready frame.
// After recovery the observer receives resync before new data. Exhaustion is
// visible through subscription-error as well as the diagnostic callback.
export class StreamSubscription {
  private lines: boolean;
  private factory: () => StreamSocket;
  private observer: (message: string) => void;
  private warn: (message: string) => void;
  private timers: StreamTimers;
  private socket: StreamSocket | undefined;
  private reconnectTimer: number | undefined;
  private readyTimer: number | undefined;
  private generation: number = 0;
  private readyGeneration: number = 0;
  private retries: number = 0;
  private cancelled: boolean = false;
  private started: boolean = false;
  private settled: boolean = false;
  private resolved: boolean = false;
  private resolveStart: ((cancel: () => void) => void) | undefined;
  private rejectStart: ((error: Error) => void) | undefined;

  constructor(lines: boolean, factory: () => StreamSocket, observer: (message: string) => void,
    warn: (message: string) => void, timers: StreamTimers) {
    this.lines = lines;
    this.factory = factory;
    this.observer = observer;
    this.warn = warn;
    this.timers = timers;
  }

  start(): Promise<() => void> {
    if (this.started) return Promise.reject(new Error('subscription already started'));
    this.started = true;
    return new Promise<() => void>((resolve, reject) => {
      this.resolveStart = resolve;
      this.rejectStart = reject;
      if (this.cancelled) {
        this.settled = true;
        reject(new Error('subscription cancelled'));
      } else {
        this.connect();
      }
    });
  }

  cancel(): void {
    if (this.cancelled) return;
    this.cancelled = true;
    this.generation++;
    this.clearTimers();
    const socket = this.socket;
    this.socket = undefined;
    socket?.close();
    if (!this.settled) {
      this.settled = true;
      this.rejectStart?.(new Error('subscription cancelled'));
    }
  }

  private clearTimers(): void {
    if (this.reconnectTimer !== undefined) this.timers.clear(this.reconnectTimer);
    if (this.readyTimer !== undefined) this.timers.clear(this.readyTimer);
    this.reconnectTimer = undefined;
    this.readyTimer = undefined;
  }

  private connect(): void {
    if (this.cancelled) return;
    const generation = ++this.generation;
    const frames = new StreamFrames(this.lines);
    try {
      const socket = this.factory();
      this.socket = socket;
      this.readyTimer = this.timers.set(() => this.failed(generation, 'RPC stream ready timed out'), 5000);
      socket.connect((text: string) => this.receive(generation, frames, text),
        (reason: string) => this.failed(generation, reason)).then(() => {
        if (this.cancelled || generation !== this.generation) { socket.close(); return; }
        if (!this.lines) this.ready(generation);
      }).catch((error: Error) => this.failed(generation, error.message ?? String(error)));
    } catch (error) {
      this.failed(generation, String(error));
    }
  }

  private receive(generation: number, frames: StreamFrames, text: string): void {
    if (this.cancelled || generation !== this.generation) return;
    try {
      for (const frame of frames.push(text)) {
        if (this.cancelled || generation !== this.generation) return;
        const envelope = JSON.parse(frame) as StreamEnvelope;
        if (envelope.error) throw new Error(envelope.error);
        if (this.lines && envelope.ready === true) { this.ready(generation); continue; }
        if (this.lines && this.readyGeneration !== generation) throw new Error('event arrived before ready');
        if (typeof envelope.result !== 'string') throw new Error('RPC stream result is not a string');
        this.deliver(envelope.result);
        if (this.cancelled || generation !== this.generation) return;
      }
    } catch (error) {
      this.failed(generation, 'RPC stream decode failed: ' + String(error));
    }
  }

  private ready(generation: number): void {
    if (this.cancelled || generation !== this.generation || this.readyGeneration === generation) return;
    this.readyGeneration = generation;
    if (this.readyTimer !== undefined) this.timers.clear(this.readyTimer);
    this.readyTimer = undefined;
    if (!this.settled) {
      this.settled = true;
      this.resolved = true;
      this.resolveStart?.(() => this.cancel());
    } else if (this.resolved && this.lines) {
      this.control('resync', null);
    }
  }

  private failed(generation: number, reason: string): void {
    if (this.cancelled || generation !== this.generation) return;
    this.generation++; // invalidate late events and duplicate error/close calls
    this.clearTimers();
    const socket = this.socket;
    this.socket = undefined;
    socket?.close();
    this.warn('RPC stream disconnected: ' + reason);
    if (this.retries >= 5) {
      this.cancelled = true;
      const message = 'RPC stream stopped after 5 reconnection attempts: ' + reason;
      this.warn(message);
      if (this.lines) this.control('subscription-error', message);
      if (!this.settled) { this.settled = true; this.rejectStart?.(new Error(message)); }
      return;
    }
    const delay = Math.min(250 * Math.pow(2, this.retries), 4000);
    this.retries++;
    this.reconnectTimer = this.timers.set(() => {
      this.reconnectTimer = undefined;
      this.connect();
    }, delay);
  }

  private control(type: string, data: string | null): void {
    const message: StreamControl = { type: type, data: data };
    this.deliver(JSON.stringify(message));
  }

  private deliver(message: string): void {
    try { this.observer(message); } catch (error) { this.warn('RPC stream observer failed: ' + String(error)); }
  }
}
