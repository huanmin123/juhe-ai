import { fork, type ChildProcess } from 'node:child_process'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { KeyedChildProcessPool, type KeyedChildProcessPoolRuntime } from '../../shared/keyed-child-process-pool.js'
import {
  modelCheckTokenPaddingMaxTokens,
  normalizeModelCheckTokenPaddingTarget,
  type ModelCheckTokenProbePrompt
} from './model-checks-token-integrity.js'

interface ModelCheckTokenWorkerOperation {
  type: 'prepare_token_probe_prompt'
  prefix: string
  targetTokens: number
}

export interface ModelCheckTokenWorkerResult extends ModelCheckTokenProbePrompt {
  workerPid: number
}

const currentModulePath = fileURLToPath(import.meta.url)
const currentModuleDir = dirname(currentModulePath)
const workerSourcePath = resolve(currentModuleDir, './model-checks-token-worker.ts')
const workerDistPath = resolve(currentModuleDir, './model-checks-token-worker.js')
const maxPrefixBytes = 16 * 1024
let tokenWorkerChild: ChildProcess | undefined
let pendingTokenWorkerRequests = 0

const tokenWorkerPool = new KeyedChildProcessPool<ModelCheckTokenWorkerOperation>({
  name: '模型检测 Token',
  createWorker: createTokenWorkerChild,
  targetSize: () => 1,
  queueMaxItems: () => 16,
  shardIndexForOperation: () => 0,
  operationType: (operation) => operation.type,
  runTimeoutMs: () => 15_000,
  slotSelection: 'least-loaded'
})

export async function prepareModelCheckTokenProbePromptInWorker(
  prefix: string,
  targetTokens: number,
  signal?: AbortSignal
): Promise<ModelCheckTokenWorkerResult> {
  if (signal?.aborted) throw new Error('模型检测 Token worker 任务已取消')
  const normalizedTarget = normalizeModelCheckTokenPaddingTarget(targetTokens)
  if (Buffer.byteLength(prefix, 'utf8') > maxPrefixBytes) {
    throw new Error(`模型检测 Token 填充前缀超过 ${maxPrefixBytes} 字节上限`)
  }
  pendingTokenWorkerRequests += 1
  refTokenWorkerChild()
  try {
    const result = await tokenWorkerPool.request({
      type: 'prepare_token_probe_prompt',
      prefix,
      targetTokens: normalizedTarget
    }) as ModelCheckTokenWorkerResult
    if (signal?.aborted) throw new Error('模型检测 Token worker 任务已取消')
    assertWorkerResult(result, normalizedTarget)
    return result
  } finally {
    pendingTokenWorkerRequests = Math.max(0, pendingTokenWorkerRequests - 1)
    if (pendingTokenWorkerRequests === 0) unrefTokenWorkerChild()
  }
}

export function getModelCheckTokenWorkerRuntime(): KeyedChildProcessPoolRuntime {
  return tokenWorkerPool.runtime()
}

export async function stopModelCheckTokenWorker(): Promise<void> {
  await tokenWorkerPool.close()
  tokenWorkerChild = undefined
  pendingTokenWorkerRequests = 0
}

function createTokenWorkerChild(): ChildProcess {
  const child = fork(resolveTokenWorkerPath(), [], {
    execArgv: tokenWorkerExecArgv(),
    stdio: ['ignore', 'ignore', 'ignore', 'ipc']
  })
  tokenWorkerChild = child
  child.once('exit', () => {
    if (tokenWorkerChild === child) tokenWorkerChild = undefined
  })
  if (pendingTokenWorkerRequests === 0) unrefTokenWorkerChild()
  return child
}

function refTokenWorkerChild(): void {
  tokenWorkerChild?.ref()
  tokenWorkerChild?.channel?.ref()
}

function unrefTokenWorkerChild(): void {
  tokenWorkerChild?.unref()
  tokenWorkerChild?.channel?.unref()
}

function resolveTokenWorkerPath(): string {
  return currentModulePath.endsWith('.ts') ? workerSourcePath : workerDistPath
}

function tokenWorkerExecArgv(): string[] {
  const execArgv = stripNodeEvalExecArgv(process.execArgv.filter((arg) => !arg.startsWith('--inspect')))
  if (!currentModulePath.endsWith('.ts') || execArgv.some((arg) => arg.includes('tsx'))) {
    return execArgv
  }
  return [...execArgv, '--import', 'tsx']
}

function stripNodeEvalExecArgv(args: string[]): string[] {
  const output: string[] = []
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index]
    if (arg === '-e' || arg === '--eval' || arg === '-p' || arg === '--print') {
      index += 1
      continue
    }
    if (arg.startsWith('--eval=') || arg.startsWith('--print=')) continue
    output.push(arg)
  }
  return output
}

function assertWorkerResult(result: ModelCheckTokenWorkerResult, targetTokens: number): void {
  if (
    !result
    || typeof result.prompt !== 'string'
    || typeof result.padding !== 'string'
    || !Number.isInteger(result.localInputTokens)
    || result.localInputTokens < targetTokens
    || !Number.isInteger(result.workerPid)
    || result.workerPid <= 0
    || targetTokens > modelCheckTokenPaddingMaxTokens
  ) {
    throw new Error('模型检测 Token worker 返回结果无效')
  }
}
