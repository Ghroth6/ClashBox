export interface VpnOperationResult {
  generation: number
  state: string
  stage: string
  error: string
  cleanupErrors: string[]
}

export class VpnRunOwner {
  generation: number
  cancelled: boolean = false
  stage: string = 'prepare'

  constructor(generation: number) {
    this.generation = generation
  }
}

export interface VpnLifecycleActions {
  prepare(owner: VpnRunOwner): Promise<void>
  create(owner: VpnRunOwner): Promise<number>
  startNative(owner: VpnRunOwner, fd: number): Promise<void>
  startListeners(owner: VpnRunOwner): void
  cancelNative(owner: VpnRunOwner): string
  closeChannel(owner: VpnRunOwner): Promise<void>
  destroy(owner: VpnRunOwner): Promise<void>
}

class VpnRunRecord {
  owner: VpnRunOwner
  work: Promise<void> = Promise.resolve()
  workSettled: boolean = false
  workTimedOut: boolean = false
  startError: string = ''
  startStage: string = ''
  startResult: Promise<VpnOperationResult> | undefined
  stopResult: Promise<VpnOperationResult> | undefined
  lastStopResult: VpnOperationResult | undefined

  constructor(owner: VpnRunOwner) {
    this.owner = owner
  }
}

class VpnCommand {
  sequence: number
  result: Promise<VpnOperationResult>

  constructor(sequence: number, result: Promise<VpnOperationResult>) {
    this.sequence = sequence
    this.result = result
  }
}

export function vpnError(error: Error): string {
  return error.message || String(error)
}

// Owns operation ordering, not SDK objects. A timeout never drops the run record.
// Work and its public Start result are distinct: Stop waits for work, while a
// failed Start may await Stop. This avoids a Start -> Stop -> Start promise cycle.
export class VpnLifecycle {
  private actions: VpnLifecycleActions
  private timeoutMs: number
  private generation: number = 0
  private state: string = 'Stopped'
  private record: VpnRunRecord | undefined
  private commands: Map<string, VpnCommand> = new Map()
  private cleanupErrors: string[] = []

  constructor(actions: VpnLifecycleActions, timeoutMs: number = 15000) {
    this.actions = actions
    this.timeoutMs = timeoutMs
  }

  execute(start: boolean, clientId: string, sequence: number): Promise<VpnOperationResult> {
    const previous = this.commands.get(clientId)
    if (previous && sequence < previous.sequence) {
      return Promise.resolve(this.snapshot('stale-command', 'VPN 操作已被较新的请求替代'))
    }
    if (previous && sequence === previous.sequence) return previous.result
    const result = start ? this.start() : this.stop()
    this.commands.set(clientId, new VpnCommand(sequence, result))
    return result
  }

  snapshot(stage: string = 'state', error: string = ''): VpnOperationResult {
    return { generation: this.record?.owner.generation ?? this.generation, state: this.state,
      stage: stage, error: error, cleanupErrors: this.cleanupErrors.slice() }
  }

  start(): Promise<VpnOperationResult> {
    const previous = this.record
    if (this.state === 'Starting' && previous?.startResult) return previous.startResult
    if (this.state === 'Running' && previous) {
      return Promise.resolve(this.result(previous, 'running', '', []))
    }
    if (previous) {
      return Promise.resolve(this.result(previous, 'start-blocked', '上轮 VPN 尚未完成清理，请重试停止', []))
    }
    const record = new VpnRunRecord(new VpnRunOwner(++this.generation))
    this.record = record
    this.state = 'Starting'
    // prepare executes synchronously up to its first await, including token capture.
    record.work = this.runStart(record)
    record.startResult = this.finishStart(record)
    return record.startResult
  }

  stop(): Promise<VpnOperationResult> {
    const record = this.record
    if (!record) {
      return Promise.resolve<VpnOperationResult>({ generation: this.generation, state: 'Stopped', stage: 'stopped', error: '', cleanupErrors: [] })
    }
    if (record.stopResult) return record.stopResult
    record.owner.cancelled = true
    this.state = 'Stopping'
    // Invalidate native tokens/protect waits BEFORE awaiting pending construction.
    let nativeError = ''
    try {
      nativeError = this.actions.cancelNative(record.owner)
    } catch (error) {
      nativeError = vpnError(error as Error)
    }
    record.stopResult = this.cleanup(record, nativeError).finally(() => {
      record.stopResult = undefined
    })
    return record.stopResult
  }

  private async runStart(record: VpnRunRecord): Promise<void> {
    const owner = record.owner
    try {
      await this.actions.prepare(owner)
      if (owner.cancelled) return
      owner.stage = 'system-create'
      const fd = await this.actions.create(owner)
      if (owner.cancelled) return
      if (fd <= 0) throw new Error('系统未返回有效的 TUN 文件描述符')
      owner.stage = 'native-start'
      await this.actions.startNative(owner, fd)
      if (owner.cancelled) return
      owner.stage = 'listeners-start'
      this.actions.startListeners(owner)
    } catch (error) {
      // A cancelled SDK/IPC waiter is the consequence of Stop, not a new startup
      // failure. Keep a pre-existing timeout/error, otherwise report cancellation.
      if (!owner.cancelled && !record.startError) {
        record.startError = vpnError(error as Error)
        record.startStage = owner.stage
      }
    } finally {
      record.workSettled = true
    }
  }

  private async finishStart(record: VpnRunRecord): Promise<VpnOperationResult> {
    const waitError = await this.wait(record.work, 'start', Date.now() + this.timeoutMs)
    if (waitError && !record.startError) {
      record.startError = waitError
      record.startStage = record.owner.stage
    }
    if (!record.owner.cancelled && !record.startError) {
      this.state = 'Running'
      return this.result(record, 'running', '', [])
    }
    // An old Start continuation must never stop a later run.
    const stopped = record.stopResult ? await record.stopResult : record.lastStopResult ??
      (this.record === record ? await this.stop() : undefined)
    return {
      generation: record.owner.generation,
      state: stopped?.state ?? 'Stopped',
      stage: record.startStage || 'start-cancelled',
      error: record.startError || 'VPN 启动已取消',
      cleanupErrors: stopped?.cleanupErrors.slice() ?? []
    }
  }

  private async cleanup(record: VpnRunRecord, nativeError: string): Promise<VpnOperationResult> {
    const deadline = Date.now() + this.timeoutMs
    const errors: string[] = []
    if (nativeError) errors.push('native-stop: ' + nativeError)
    // Closing the channel also rejects the pending native-ready waiter.
    let channel: Promise<void>
    try {
      channel = this.actions.closeChannel(record.owner)
    } catch (error) {
      channel = Promise.reject(error)
    }
    // Attach an observer immediately, even while waiting for create to finish.
    const channelResult = this.wait(channel, 'protect-close', deadline)
    const workError = await this.wait(record.work, 'start-settle', deadline)
    if (workError) errors.push(workError)
    const channelError = await channelResult
    if (channelError) errors.push(channelError)
    if (record.workSettled) {
      try {
        const destroyError = await this.wait(this.actions.destroy(record.owner), 'system-destroy', deadline)
        if (destroyError) errors.push(destroyError)
      } catch (error) {
        errors.push('system-destroy: ' + vpnError(error as Error))
      }
    } else if (!record.workTimedOut) {
      record.workTimedOut = true
      // A create that finishes AFTER our bounded response still needs destruction.
      // Re-enter cleanup once on settlement; failed cleanup is left for explicit retry.
      record.work.then(async () => {
        if (record.stopResult) await record.stopResult
        if (this.record === record && record.owner.cancelled) await this.stop()
      })
    }
    if (errors.length === 0) {
      this.state = 'Stopped'
      this.record = undefined
      this.cleanupErrors = []
      record.lastStopResult = this.result(record, 'stopped', '', [])
      return record.lastStopResult
    }
    this.state = 'CleanupFailed'
    this.cleanupErrors = errors.slice()
    record.lastStopResult = this.result(record, 'cleanup', 'VPN 清理未完成', errors)
    return record.lastStopResult
  }

  private result(record: VpnRunRecord, stage: string, error: string, cleanupErrors: string[]): VpnOperationResult {
    return { generation: record.owner.generation, state: this.state, stage: stage, error: error, cleanupErrors: cleanupErrors }
  }

  private wait(work: Promise<void>, stage: string, deadline: number): Promise<string> {
    return new Promise<string>((resolve) => {
      const timer = setTimeout(() => resolve(stage + ': 等待超时，结果未知'), Math.max(0, deadline - Date.now()))
      work.then(() => {
        clearTimeout(timer)
        resolve('')
      }, (error: Error) => {
        clearTimeout(timer)
        resolve(stage + ': ' + vpnError(error))
      })
    })
  }
}
