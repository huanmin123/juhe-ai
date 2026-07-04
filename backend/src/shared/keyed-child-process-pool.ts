import type { ChildProcess } from 'node:child_process'

import { errorLogFields, logger } from './logger.js'

export interface KeyedChildProcessPoolRuntime {
  workerCount: number
  queueLength: number
  activeJobs: number
  handledJobs: number
  failedJobs: number
  rejectedJobs: number
  timedOutJobs: number
  restartedWorkers: number
  oldestQueuedMs: number
  maxQueueWaitMs: number
  maxRunMs: number
}

export interface KeyedChildProcessPoolResponse {
  requestId: string
  ok: boolean
  result?: unknown
  errorMessage?: string
}

export interface KeyedChildProcessPoolOptions<Operation> {
  name: string
  createWorker: (slotIndex: number) => ChildProcess
  targetSize: () => number
  queueMaxItems: () => number
  shardIndexForOperation: (operation: Operation) => number
  operationType?: (operation: Operation) => string
  runTimeoutMs?: () => number
  slotSelection?: 'keyed' | 'least-loaded'
}

interface KeyedChildProcessPoolJob<Operation> {
  id: string
  operation: Operation
  queuedAt: number
  startedAt?: number
  timeout?: NodeJS.Timeout
  resolve: (value: unknown) => void
  reject: (error: Error) => void
}

interface KeyedChildProcessPoolSlot<Operation> {
  index: number
  worker?: ChildProcess
  queue: Array<KeyedChildProcessPoolJob<Operation>>
  active?: KeyedChildProcessPoolJob<Operation>
}

export class KeyedChildProcessPool<Operation> {
  private nextJobId = 1
  private slots: Array<KeyedChildProcessPoolSlot<Operation>> = []
  private handledJobs = 0
  private failedJobs = 0
  private rejectedJobs = 0
  private timedOutJobs = 0
  private restartedWorkers = 0
  private maxQueueWaitMs = 0
  private maxRunMs = 0
  private exclusiveBarrier: Promise<unknown> = Promise.resolve()
  private idleWaiters: Array<() => void> = []
  private poolGeneration = 0

  constructor(private readonly options: KeyedChildProcessPoolOptions<Operation>) {}

  runtime(): KeyedChildProcessPoolRuntime {
    return {
      workerCount: this.slots.filter((slot) => Boolean(slot.worker)).length,
      queueLength: this.slots.reduce((sum, slot) => sum + slot.queue.length, 0),
      activeJobs: this.slots.filter((slot) => Boolean(slot.active)).length,
      handledJobs: this.handledJobs,
      failedJobs: this.failedJobs,
      rejectedJobs: this.rejectedJobs,
      timedOutJobs: this.timedOutJobs,
      restartedWorkers: this.restartedWorkers,
      oldestQueuedMs: this.oldestQueuedMs(),
      maxQueueWaitMs: Math.round(this.maxQueueWaitMs),
      maxRunMs: Math.round(this.maxRunMs)
    }
  }

  async close(): Promise<void> {
    this.poolGeneration += 1
    const closing = this.slots
    this.slots = []
    this.exclusiveBarrier = Promise.resolve()
    await Promise.allSettled(closing.map(async (slot) => {
      this.failSlotJobs(slot, new Error(`${this.options.name} writer pool 已关闭`))
      if (slot.worker) {
        await stopWriterChild(slot.worker)
      }
    }))
    this.handledJobs = 0
    this.failedJobs = 0
    this.rejectedJobs = 0
    this.timedOutJobs = 0
    this.restartedWorkers = 0
    this.maxQueueWaitMs = 0
    this.maxRunMs = 0
    this.notifyIdle()
  }

  async request(operation: Operation): Promise<unknown> {
    await this.exclusiveBarrier.catch(() => undefined)
    return await this.enqueue(operation)
  }

  requestExclusive(operation: Operation): Promise<unknown> {
    const run = this.exclusiveBarrier
      .catch(() => undefined)
      .then(async () => {
        this.ensurePool()
        await this.waitForIdle()
        return await this.enqueue(operation)
      })
    this.exclusiveBarrier = run.catch(() => undefined)
    return run
  }

  private async enqueue(operation: Operation): Promise<unknown> {
    this.ensurePool()
    const totalQueued = this.slots.reduce((sum, slot) => sum + slot.queue.length, 0)
    if (totalQueued >= this.options.queueMaxItems()) {
      this.rejectedJobs += 1
      throw new Error(`${this.options.name} writer pool 队列已满`)
    }
    const slot = this.slotForOperation(operation)
    return await new Promise((resolve, reject) => {
      slot.queue.push({
        id: String(this.nextJobId++),
        operation,
        queuedAt: Date.now(),
        resolve,
        reject
      })
      this.pumpSlot(slot)
    })
  }

  private ensurePool(): void {
    const targetSize = Math.max(1, Math.trunc(this.options.targetSize()))
    while (this.slots.length < targetSize) {
      this.slots.push({
        index: this.slots.length,
        queue: []
      })
    }
    for (const slot of this.slots) {
      this.ensureSlotWorker(slot)
    }
  }

  private ensureSlotWorker(slot: KeyedChildProcessPoolSlot<Operation>): ChildProcess {
    if (slot.worker && slot.worker.exitCode === null && !slot.worker.killed) {
      return slot.worker
    }
    const worker = this.options.createWorker(slot.index)
    slot.worker = worker
    worker.on('message', (message: KeyedChildProcessPoolResponse) => this.handleWorkerMessage(slot, message))
    worker.on('error', (error) => {
      logger.warn(errorLogFields(error, {
        event: 'keyed_child_process_pool_worker_error',
        poolName: this.options.name,
        workerSlot: slot.index
      }), `${this.options.name} writer worker 异常`)
      this.failSlotJobs(slot, error)
    })
    worker.on('exit', (code) => {
      if (code !== 0) {
        logger.warn({
          event: 'keyed_child_process_pool_worker_exited',
          poolName: this.options.name,
          workerSlot: slot.index,
          exitCode: code
        }, `${this.options.name} writer worker 异常退出`)
      }
      if (slot.worker !== worker) {
        return
      }
      slot.worker = undefined
      this.failSlotJobs(slot, new Error(`${this.options.name} writer worker 已退出，退出码 ${code}`))
    })
    return worker
  }

  private pumpSlot(slot: KeyedChildProcessPoolSlot<Operation>): void {
    if (slot.active || slot.queue.length === 0) {
      return
    }
    const job = slot.queue.shift()
    if (!job) return
    slot.active = job
    job.startedAt = Date.now()
    this.maxQueueWaitMs = Math.max(this.maxQueueWaitMs, job.startedAt - job.queuedAt)
    try {
      const worker = this.ensureSlotWorker(slot)
      this.startJobTimeout(slot, job)
      worker.send({
        requestId: job.id,
        operation: job.operation
      }, (error) => {
        if (!error) return
        if (slot.active === job) {
          slot.active = undefined
        }
        this.clearJobTimeout(job)
        this.failJob(job, error)
        this.pumpSlot(slot)
        this.notifyIdle()
      })
    } catch (error) {
      slot.active = undefined
      this.clearJobTimeout(job)
      this.failJob(job, error)
      this.pumpSlot(slot)
    }
  }

  private handleWorkerMessage(slot: KeyedChildProcessPoolSlot<Operation>, message: KeyedChildProcessPoolResponse): void {
    const job = slot.active
    if (!job || job.id !== message.requestId) {
      return
    }
    slot.active = undefined
    this.clearJobTimeout(job)
    this.recordRunDuration(job)
    if (message.ok) {
      this.handledJobs += 1
      job.resolve(message.result)
    } else {
      this.failJob(job, new Error(message.errorMessage ?? `${this.options.name} writer 操作失败`))
    }
    this.pumpSlot(slot)
    this.notifyIdle()
  }

  private failSlotJobs(slot: KeyedChildProcessPoolSlot<Operation>, error: Error): void {
    if (slot.active) {
      this.clearJobTimeout(slot.active)
      this.failJob(slot.active, error)
      slot.active = undefined
    }
    while (slot.queue.length > 0) {
      const job = slot.queue.shift()
      if (job) this.failJob(job, error)
    }
    this.notifyIdle()
  }

  private failJob(job: KeyedChildProcessPoolJob<Operation>, error: unknown): void {
    this.clearJobTimeout(job)
    this.recordRunDuration(job)
    this.failedJobs += 1
    job.reject(error instanceof Error ? error : new Error(String(error)))
  }

  private startJobTimeout(slot: KeyedChildProcessPoolSlot<Operation>, job: KeyedChildProcessPoolJob<Operation>): void {
    this.clearJobTimeout(job)
    const timeoutMs = this.jobRunTimeoutMs()
    if (timeoutMs <= 0) {
      return
    }
    job.timeout = setTimeout(() => {
      if (slot.active !== job) {
        return
      }
      job.timeout = undefined
      slot.active = undefined
      this.recordRunDuration(job)
      this.failedJobs += 1
      this.timedOutJobs += 1
      const operationType = this.options.operationType?.(job.operation)
      logger.warn({
        event: 'keyed_child_process_pool_job_timeout',
        poolName: this.options.name,
        workerSlot: slot.index,
        jobId: job.id,
        operationType,
        timeoutMs,
        runMs: job.startedAt === undefined ? undefined : Date.now() - job.startedAt
      }, `${this.options.name} writer 操作超时，已重启对应 worker`)
      job.reject(new Error(`${this.options.name} writer 操作超时 ${timeoutMs}ms`))
      this.restartSlotWorker(slot)
      this.notifyIdle()
    }, timeoutMs)
    job.timeout.unref()
  }

  private clearJobTimeout(job: KeyedChildProcessPoolJob<Operation>): void {
    if (!job.timeout) {
      return
    }
    clearTimeout(job.timeout)
    job.timeout = undefined
  }

  private restartSlotWorker(slot: KeyedChildProcessPoolSlot<Operation>): void {
    const generation = this.poolGeneration
    const worker = slot.worker
    if (worker) {
      slot.worker = undefined
      this.restartedWorkers += 1
      void stopWriterChild(worker).finally(() => {
        if (this.poolGeneration !== generation || !this.slots.includes(slot)) {
          return
        }
        this.ensureSlotWorker(slot)
        this.pumpSlot(slot)
        this.notifyIdle()
      })
      return
    }
    if (!this.slots.includes(slot)) {
      return
    }
    this.ensureSlotWorker(slot)
    this.pumpSlot(slot)
  }

  private jobRunTimeoutMs(): number {
    const rawValue = this.options.runTimeoutMs?.() ?? 30_000
    if (!Number.isFinite(rawValue)) {
      return 30_000
    }
    return Math.max(0, Math.trunc(rawValue))
  }

  private recordRunDuration(job: KeyedChildProcessPoolJob<Operation>): void {
    if (job.startedAt === undefined) return
    this.maxRunMs = Math.max(this.maxRunMs, Date.now() - job.startedAt)
  }

  private waitForIdle(): Promise<void> {
    if (this.isIdle()) {
      return Promise.resolve()
    }
    return new Promise((resolve) => {
      this.idleWaiters.push(resolve)
    })
  }

  private notifyIdle(): void {
    if (!this.isIdle()) {
      return
    }
    while (this.idleWaiters.length > 0) {
      const resolve = this.idleWaiters.shift()
      resolve?.()
    }
  }

  private isIdle(): boolean {
    return this.slots.every((slot) => !slot.active && slot.queue.length === 0)
  }

  private oldestQueuedMs(): number {
    let oldestAt = 0
    for (const slot of this.slots) {
      for (const job of slot.queue) {
        if (oldestAt === 0 || job.queuedAt < oldestAt) {
          oldestAt = job.queuedAt
        }
      }
    }
    return oldestAt === 0 ? 0 : Math.max(0, Date.now() - oldestAt)
  }

  private slotForOperation(operation: Operation): KeyedChildProcessPoolSlot<Operation> {
    if (this.options.slotSelection === 'least-loaded') {
      const slot = this.leastLoadedSlot()
      if (!slot) {
        const operationType = this.options.operationType?.(operation)
        throw new Error(`${this.options.name} writer pool 未初始化${operationType ? `：${operationType}` : ''}`)
      }
      return slot
    }
    const shardIndex = this.options.shardIndexForOperation(operation)
    const slot = this.slots[shardIndex % Math.max(1, this.slots.length)]
    if (!slot) {
      const operationType = this.options.operationType?.(operation)
      throw new Error(`${this.options.name} writer pool 未初始化${operationType ? `：${operationType}` : ''}`)
    }
    return slot
  }

  private leastLoadedSlot(): KeyedChildProcessPoolSlot<Operation> | undefined {
    return this.slots.reduce<KeyedChildProcessPoolSlot<Operation> | undefined>((selected, slot) => {
      if (!selected) return slot
      const slotLoad = slot.queue.length + (slot.active ? 1 : 0)
      const selectedLoad = selected.queue.length + (selected.active ? 1 : 0)
      return slotLoad < selectedLoad ? slot : selected
    }, undefined)
  }
}

async function stopWriterChild(child: ChildProcess): Promise<void> {
  if (child.killed || child.exitCode !== null) {
    return
  }
  await new Promise<void>((resolve) => {
    const timer = setTimeout(() => {
      child.kill()
      resolve()
    }, 2000)
    child.once('exit', () => {
      clearTimeout(timer)
      resolve()
    })
    try {
      if (child.connected) {
        child.disconnect()
      } else {
        child.kill()
      }
    } catch {
      child.kill()
    }
  })
}
