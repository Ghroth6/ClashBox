import { connection } from '@kit.NetworkKit';
import { publishNetworkSnapshot } from 'libflclash.so';

export interface PlatformRoute {
  destination: string;
  gateway?: string;
}

export interface PlatformInterface {
  name: string;
  index: number;
  mtu: number;
  up: boolean;
  addresses: string[];
  routes: PlatformRoute[];
}

export interface PlatformNetworkSnapshot {
  generation: number;
  networkId: number;
  online: boolean;
  dns: string[];
  interfaces: PlatformInterface[];
}

export interface NetworkSnapshotDependencies {
  createConnection(): connection.NetConnection;
  getDefaultNet(): Promise<connection.NetHandle>;
  getAllNets(): Promise<connection.NetHandle[]>;
  getCapabilities(handle: connection.NetHandle): Promise<connection.NetCapabilities>;
  getProperties(handle: connection.NetHandle): Promise<connection.ConnectionProperties>;
  publish(json: string): string;
  log(message: string): void;
}

class NativeNetworkDependencies implements NetworkSnapshotDependencies {
  createConnection(): connection.NetConnection { return connection.createNetConnection(); }
  getDefaultNet(): Promise<connection.NetHandle> { return connection.getDefaultNet(); }
  getAllNets(): Promise<connection.NetHandle[]> { return connection.getAllNets(); }
  getCapabilities(handle: connection.NetHandle): Promise<connection.NetCapabilities> {
    return connection.getNetCapabilities(handle);
  }
  getProperties(handle: connection.NetHandle): Promise<connection.ConnectionProperties> {
    return connection.getConnectionProperties(handle);
  }
  publish(json: string): string { return publishNetworkSnapshot(json); }
  log(message: string): void { console.error('[PlatformNetwork] ' + message); }
}

// Native state survives a monitor stop/start. Never restart its generation at 1.
let generation = 0;
function nextGeneration(): number {
  generation++;
  if (!Number.isSafeInteger(generation)) throw new Error('Network generation exhausted');
  return generation;
}

function offlineSnapshot(value: number): PlatformNetworkSnapshot {
  return { generation: value, networkId: 0, online: false, dns: [], interfaces: [] };
}

function validIPv4(value: string): boolean {
  const parts = value.split('.');
  return parts.length === 4 && parts.every((part: string) =>
    /^(0|[1-9][0-9]{0,2})$/.test(part) && Number(part) <= 255);
}

function validIPv6(value: string): boolean {
  if (!/^[0-9a-fA-F:.]+$/.test(value)) return false;
  const compression = value.indexOf('::');
  if (compression >= 0 && value.indexOf('::', compression + 2) >= 0) return false;
  const sides = compression >= 0 ? value.split('::') : [value];
  let groups = 0;
  for (let sideIndex = 0; sideIndex < sides.length; sideIndex++) {
    if (sides[sideIndex] === '') continue;
    const parts = sides[sideIndex].split(':');
    for (let index = 0; index < parts.length; index++) {
      const part = parts[index];
      if (part.includes('.')) {
        if (sideIndex !== sides.length - 1 || index !== parts.length - 1 || !validIPv4(part)) return false;
        groups += 2;
      } else {
        if (!/^[0-9a-fA-F]{1,4}$/.test(part)) return false;
        groups++;
      }
    }
  }
  return compression >= 0 ? groups < 8 : groups === 8;
}

function addressValue(address: connection.NetAddress, allowZone: boolean): string {
  const family = address.family === undefined ? 1 : address.family;
  const value = address.address;
  if (typeof value !== 'string' || value === '' || value.trim() !== value) throw new Error('Invalid network address');
  if (family === 1 && validIPv4(value)) return value;
  if (family === 2) {
    const parts = value.split('%');
    if (parts.length > 2 || (parts.length === 2 && (!allowZone || !/^[A-Za-z0-9_.-]+$/.test(parts[1])))) {
      throw new Error('Invalid IPv6 address zone');
    }
    if (validIPv6(parts[0])) return value;
  }
  throw new Error('Network address does not match SDK family 1/2');
}

function prefixValue(link: connection.LinkAddress): string {
  const address = addressValue(link.address, false);
  const bits = link.address.family === 2 ? 128 : 32;
  if (!Number.isInteger(link.prefixLength) || link.prefixLength < 0 || link.prefixLength > bits) {
    throw new Error('Invalid network prefix length');
  }
  return address + '/' + link.prefixLength;
}

// Build a private representation from ConnectionProperties, never a neighbour
// cache. Core publication additionally parses and validates the numeric values.
export function buildNetworkSnapshot(networkId: number, properties: connection.ConnectionProperties,
  value: number): PlatformNetworkSnapshot {
  if (!Number.isSafeInteger(networkId) || networkId <= 0) throw new Error('Invalid default network id');
  if (!Number.isSafeInteger(value) || value <= 0) throw new Error('Invalid network generation');
  if (typeof properties.interfaceName !== 'string' || properties.interfaceName.trim() !== properties.interfaceName ||
    properties.interfaceName === '') throw new Error('Missing network interface name');
  if (!Number.isInteger(properties.mtu) || properties.mtu < 0) throw new Error('Invalid interface MTU');
  if (!Array.isArray(properties.linkAddresses) || properties.linkAddresses.length === 0 ||
    !Array.isArray(properties.dnses) || !Array.isArray(properties.routes)) throw new Error('Incomplete connection properties');

  const addresses: string[] = properties.linkAddresses.map((link: connection.LinkAddress) => prefixValue(link));
  const dns: string[] = properties.dnses.map((item: connection.NetAddress) => {
    const address = addressValue(item, true);
    const port = item.port === undefined || item.port === 0 ? 53 : item.port;
    if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Invalid DNS port');
    return (item.family === 2 ? '[' + address + ']' : address) + ':' + port;
  });
  const routes: PlatformRoute[] = [];
  properties.routes.forEach((route: connection.RouteInfo) => {
    if (route.isExcludedRoute === true) return;
    if (route.interface !== properties.interfaceName) throw new Error('Route belongs to a different interface');
    const output: PlatformRoute = { destination: prefixValue(route.destination) };
    if (route.hasGateway) {
      if ((route.gateway.family === undefined ? 1 : route.gateway.family) !==
        (route.destination.address.family === undefined ? 1 : route.destination.address.family)) {
        throw new Error('Route gateway family mismatch');
      }
      output.gateway = addressValue(route.gateway, true);
    }
    routes.push(output);
  });
  // ConnectionProperties does not expose an OS ifindex. Zero explicitly means
  // unknown; the network handle's netId must never be used as that index.
  const iface: PlatformInterface = {
    name: properties.interfaceName, index: 0, mtu: properties.mtu, up: true, addresses: addresses, routes: routes
  };
  return { generation: value, networkId: networkId, online: true, dns: dns, interfaces: [iface] };
}

class NetworkRun {
  listener: connection.NetConnection;
  registered: boolean = false;
  disposed: boolean = false;
  released: boolean = false;
  initializing: boolean = true;
  pending: boolean = false;
  revision: number = 0;
  requiredWaiters: number = 0;
  starting: Promise<void> | undefined;
  worker: Promise<void> | undefined;
  constructor(listener: connection.NetConnection) { this.listener = listener; }
}

export class PlatformNetworkMonitor {
  private dependencies: NetworkSnapshotDependencies;
  private run: NetworkRun | undefined;

  constructor(dependencies: NetworkSnapshotDependencies = new NativeNetworkDependencies()) {
    this.dependencies = dependencies;
  }

  start(): Promise<void> {
    if (this.run !== undefined) return this.run.starting === undefined ? this.prepare(this.run) : this.run.starting;
    let run: NetworkRun;
    try { run = new NetworkRun(this.dependencies.createConnection()); }
    catch (error) { this.publishOffline(); return Promise.reject(error); }
    this.run = run;
    run.starting = this.initialize(run);
    return run.starting;
  }

  stop(): void {
    const run = this.run;
    this.run = undefined;
    if (run !== undefined) this.dispose(run);
    this.publishOffline();
  }

  private current(run: NetworkRun): boolean { return this.run === run && !run.disposed; }

  // An already active monitor refreshes on explicit start (for a new VPN run).
  // Concurrent requests share collection while each caller waits for settlement.
  private async prepare(run: NetworkRun): Promise<void> {
    run.requiredWaiters++;
    run.revision++;
    run.pending = true;
    try {
      do {
        await this.refresh(run);
        if (!this.current(run)) throw new Error('Network monitor stopped during refresh');
      } while (run.pending || run.worker !== undefined);
    } finally {
      run.requiredWaiters--;
    }
  }

  private async initialize(run: NetworkRun): Promise<void> {
    try {
      await new Promise<void>((resolve, reject) => {
        run.listener.register((error) => {
          if (error) { reject(new Error('Network listener registration failed: ' + JSON.stringify(error))); return; }
          run.registered = true;
          if (run.disposed) this.release(run);
          resolve();
        });
      });
      if (!this.current(run)) throw new Error('Network monitor stopped during registration');
      // Subscribe after register as required by the SDK's on(...) declarations.
      // The explicit first collection also covers changes during registration.
      run.listener.on('netAvailable', () => this.changed(run, false));
      run.listener.on('netCapabilitiesChange', () => this.changed(run, false));
      run.listener.on('netConnectionPropertiesChange', () => this.changed(run, false));
      run.listener.on('netLost', () => this.changed(run, true));
      run.listener.on('netUnavailable', () => this.changed(run, true));
      run.revision++;
      run.pending = true;
      await this.refresh(run);
      if (!this.current(run)) throw new Error('Network monitor stopped during startup');
      run.initializing = false;
      run.starting = undefined;
      if (run.pending) this.background(run);
    } catch (error) {
      if (this.current(run)) {
        this.run = undefined;
        this.dispose(run);
        this.publishOffline();
      }
      throw error;
    }
  }

  private changed(run: NetworkRun, lost: boolean): void {
    if (!this.current(run)) return;
    run.revision++;
    run.pending = true;
    if (lost) this.publishOffline();
    if (!run.initializing) this.background(run);
  }

  private background(run: NetworkRun): void {
    if (run.worker !== undefined) return;
    this.refresh(run).catch((error: Error) => {
      if (!this.current(run)) return;
      this.dependencies.log('Network refresh failed: ' + error.message);
    });
  }

  private refresh(run: NetworkRun): Promise<void> {
    if (run.worker !== undefined) return run.worker;
    const worker = this.drain(run);
    run.worker = worker;
    const finished = () => {
      if (run.worker === worker) run.worker = undefined;
      if (this.current(run) && !run.initializing && run.pending) this.background(run);
    };
    worker.then(finished, finished);
    return worker;
  }

  private async drain(run: NetworkRun): Promise<void> {
    while (this.current(run) && run.pending) {
      run.pending = false;
      const revision = run.revision;
      const value = nextGeneration();
      try {
        const snapshot = await this.collect(value);
        if (!this.current(run)) throw new Error('Network monitor stopped during collection');
        if (revision !== run.revision) continue;
        this.publish(snapshot);
      } catch (error) {
        if (this.current(run) && revision !== run.revision) continue;
        if (!this.current(run) || run.initializing) throw error;
        this.dependencies.log('Network refresh failed: ' + (error as Error).message);
        this.publishOffline();
        if (run.requiredWaiters > 0) throw error;
      }
    }
  }

  private async collect(value: number): Promise<PlatformNetworkSnapshot> {
    for (let attempt = 0; attempt < 3; attempt++) {
      const before = await this.dependencies.getDefaultNet();
      if (!Number.isSafeInteger(before.netId) || before.netId < 0) throw new Error('Invalid default network handle');
      let snapshot = offlineSnapshot(value);
      if (before.netId !== 0) {
        // getAllNets returns activated/connected networks. INTERNET capability
        // plus a non-VPN bearer is the inclusion criterion, not a reachability
        // claim. The no-specifier listener is passive and only watches default
        // network events; other interfaces refresh on those events or start().
        const handles = await this.dependencies.getAllNets();
        const others: connection.NetHandle[] = [];
        handles.forEach((handle: connection.NetHandle) => {
          if (!Number.isSafeInteger(handle.netId) || handle.netId <= 0) throw new Error('Invalid connected network handle');
          if (handle.netId !== before.netId && !others.some((saved: connection.NetHandle) => saved.netId === handle.netId)) {
            others.push(handle);
          }
        });
        others.sort((left: connection.NetHandle, right: connection.NetHandle) => left.netId - right.netId);
        others.push(before);
        let interfaces: PlatformInterface[] = [];
        for (let index = 0; index < others.length; index++) {
          const handle = others[index];
          const caps = await this.dependencies.getCapabilities(handle);
          if (!Array.isArray(caps.bearerTypes)) throw new Error('Missing network bearer types');
          const internet = caps.networkCap !== undefined && caps.networkCap.includes(connection.NetCap.NET_CAPABILITY_INTERNET);
          const vpn = caps.bearerTypes.includes(connection.NetBearType.BEARER_VPN);
          if (!internet || vpn) continue;
          const observed = buildNetworkSnapshot(handle.netId, await this.dependencies.getProperties(handle), value);
          const iface = observed.interfaces[0];
          // Deduplicate names deterministically; the default interface is last
          // so core's equal-prefix route insertion gives it final precedence.
          interfaces = interfaces.filter((saved: PlatformInterface) => saved.name !== iface.name);
          interfaces.push(iface);
          if (handle.netId === before.netId) snapshot = observed;
        }
        if (snapshot.online) snapshot.interfaces = interfaces;
      }
      const after = await this.dependencies.getDefaultNet();
      if (before.netId === after.netId) return snapshot;
    }
    throw new Error('Default network kept changing during collection');
  }

  private publish(snapshot: PlatformNetworkSnapshot): void {
    const error = this.dependencies.publish(JSON.stringify(snapshot));
    if (error !== '') throw new Error('Native network publication failed: ' + error);
  }

  private publishOffline(): void {
    try { this.publish(offlineSnapshot(nextGeneration())); }
    catch (error) { this.dependencies.log('Unable to publish offline network: ' + (error as Error).message); }
  }

  private dispose(run: NetworkRun): void {
    run.disposed = true;
    run.pending = false;
    run.revision++;
    if (run.registered) this.release(run);
  }

  private release(run: NetworkRun): void {
    if (run.released) return;
    run.released = true;
    try {
      run.listener.unregister((error) => {
        if (error) this.dependencies.log('Network listener unregister failed: ' + JSON.stringify(error));
      });
    } catch (error) {
      this.dependencies.log('Network listener unregister failed: ' + (error as Error).message);
    }
  }
}
