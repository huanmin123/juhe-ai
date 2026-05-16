import { Worker } from 'node:worker_threads'

import { errorLogFields, logger } from '../../shared/logger.js'

interface JsonParseJob {
  id: number
  rawBody: Buffer
  resolve: (value: unknown) => void
  reject: (error: Error) => void
  timeoutMs: number
  signal?: AbortSignal
  abortListener?: () => void
  timer?: NodeJS.Timeout
}

interface JsonParseWorkerMessage {
  id: number
  ok: boolean
  value?: unknown
  errorMessage?: string
}

const workerSource = `
import { parentPort } from 'node:worker_threads'

parentPort.on('message', (message) => {
  const id = message.id
  try {
    const rawBody = Buffer.from(message.rawBody)
    const value = JSON.parse(rawBody.toString('utf8'))
    parentPort.postMessage({ id, ok: true, value })
  } catch (error) {
    parentPort.postMessage({
      id,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
})
`

let worker: Worker | undefined
let nextJobId = 1
let activeJob: JsonParseJob | undefined
const queuedJobs: JsonParseJob[] = []

export function parseGatewayJsonBodyInWorker(rawBody: Buffer, timeoutMs = 30000, signal?: AbortSignal): Promise<unknown> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new Error('网关 JSON 解析已取消'))
      return
    }
    const job: JsonParseJob = {
      id: nextJobId++,
      rawBody,
      resolve,
      reject,
      timeoutMs,
      signal
    }
    if (signal) {
      job.abortListener = () => cancelJob(job)
      signal.addEventListener('abort', job.abortListener, { once: true })
    }
    queuedJobs.push(job)
    pumpJsonParseQueue()
  })
}

export async function stopGatewayJsonParseWorker(): Promise<void> {
  const currentWorker = worker
  const currentJob = activeJob
  worker = undefined
  activeJob = undefined
  if (currentJob) {
    clearJobTimer(currentJob)
    removeAbortListener(currentJob)
    currentJob.reject(new Error('网关 JSON 解析 worker 已关闭'))
  }
  while (queuedJobs.length > 0) {
    const job = queuedJobs.shift()
    if (job) {
      clearJobTimer(job)
      removeAbortListener(job)
      job.reject(new Error('网关 JSON 解析 worker 已关闭'))
    }
  }
  if (currentWorker) {
    currentWorker.removeAllListeners()
    await currentWorker.terminate()
  }
}

function pumpJsonParseQueue(): void {
  if (activeJob || queuedJobs.length === 0) {
    return
  }

  const job = queuedJobs.shift()
  if (!job) {
    return
  }
  activeJob = job

  try {
    startJobTimer(job)
    ensureWorker().postMessage({ id: job.id, rawBody: job.rawBody })
  } catch (error) {
    failJob(job, error instanceof Error ? error : new Error(String(error)), true)
  }
}

function ensureWorker(): Worker {
  if (worker) {
    return worker
  }

  worker = new Worker(workerSource, { eval: true })
  worker.unref()
  worker.on('message', handleWorkerMessage)
  worker.on('error', (error) => {
    failActiveJob(error, true)
  })
  worker.on('exit', (code) => {
    const exitedWorker = worker
    worker = undefined
    if (code !== 0) {
      logger.warn({
        event: 'gateway_json_parse_worker_exited',
        exitCode: code
      }, '网关 JSON 解析 worker 异常退出')
    }
    if (exitedWorker && activeJob) {
      failActiveJob(new Error(`网关 JSON 解析 worker 已退出，退出码 ${code}`), false)
    }
    pumpJsonParseQueue()
  })
  return worker
}

function handleWorkerMessage(message: JsonParseWorkerMessage): void {
  const job = activeJob
  if (!job || message.id !== job.id) {
    return
  }

  clearJobTimer(job)
  removeAbortListener(job)
  activeJob = undefined
  if (message.ok) {
    job.resolve(message.value)
  } else {
    job.reject(new Error(message.errorMessage ?? '网关 JSON 请求体必须是有效 JSON'))
  }
  pumpJsonParseQueue()
}

function failActiveJob(error: Error, restartWorker: boolean): void {
  if (activeJob) {
    failJob(activeJob, error, restartWorker)
    return
  }
  if (restartWorker) {
    restartJsonParseWorker()
  }
}

function failJob(job: JsonParseJob, error: Error, restartWorker: boolean): void {
  const wasActive = activeJob?.id === job.id
  if (wasActive) {
    activeJob = undefined
  } else {
    const queuedIndex = queuedJobs.findIndex((item) => item.id === job.id)
    if (queuedIndex >= 0) {
      queuedJobs.splice(queuedIndex, 1)
    }
  }
  clearJobTimer(job)
  removeAbortListener(job)
  logger.warn(errorLogFields(error, {
    event: 'gateway_json_parse_worker_failed',
    jobId: job.id,
    rawBodyBytes: job.rawBody.byteLength,
    queuedJobs: queuedJobs.length
  }), '网关 JSON 解析 worker 失败')
  job.reject(error)
  if (restartWorker) {
    restartJsonParseWorker()
  }
  pumpJsonParseQueue()
}

function cancelJob(job: JsonParseJob): void {
  const wasActive = activeJob?.id === job.id
  if (wasActive) {
    activeJob = undefined
  } else {
    const queuedIndex = queuedJobs.findIndex((item) => item.id === job.id)
    if (queuedIndex >= 0) {
      queuedJobs.splice(queuedIndex, 1)
    }
  }
  clearJobTimer(job)
  removeAbortListener(job)
  job.reject(new Error('网关 JSON 解析已取消'))
  if (wasActive) {
    restartJsonParseWorker()
  }
  pumpJsonParseQueue()
}

function startJobTimer(job: JsonParseJob): void {
  clearJobTimer(job)
  job.timer = setTimeout(() => {
    failJob(job, new Error(`网关 JSON 解析 worker ${job.timeoutMs}ms 超时`), true)
  }, job.timeoutMs)
  job.timer.unref()
}

function clearJobTimer(job: JsonParseJob): void {
  if (!job.timer) {
    return
  }
  clearTimeout(job.timer)
  job.timer = undefined
}

function removeAbortListener(job: JsonParseJob): void {
  if (!job.signal || !job.abortListener) {
    return
  }
  job.signal.removeEventListener('abort', job.abortListener)
  job.abortListener = undefined
}

function restartJsonParseWorker(): void {
  const currentWorker = worker
  worker = undefined
  if (currentWorker) {
    currentWorker.removeAllListeners()
    void currentWorker.terminate().catch(() => undefined)
  }
}
