import { reactive, watch } from 'vue'

import type {
  ActiveModelCheckRunSummary,
  ModelCheckProgressEvent,
  ModelCheckRunDetail
} from '@/types/domain'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatClockTime } from './modelCheckFormatters'
import type { ModelCheckTerminalLine } from './ModelCheckTerminal.vue'

export class ModelCheckSessionBusyError extends Error {
  constructor() {
    super('当前已有模型检测正在运行，请等待完成或先手动停止')
    this.name = 'ModelCheckSessionBusyError'
  }
}

export const modelCheckRunSessionStorageTtlMs = 12 * 60 * 60 * 1000

const modelCheckRunSessionStorageKey = 'juhe-ai:model-check-run-session:v1'
const modelCheckTerminalRestoredText = '已恢复本地终端记录；旧进度流不会回放，后端检测完成后可在历史记录中查看报告'
const modelCheckTerminalInactiveText = '本地终端记录已恢复，但后端没有运行中的检测任务；请刷新历史记录查看最终报告'

export const modelCheckRunSession = reactive({
  ...readStoredModelCheckRunSessionState()
})

let terminalLineId = maxTerminalLineId(modelCheckRunSession.terminalLines)
let activeAbortController: AbortController | undefined
let activeRunSerial = 0

watch(modelCheckRunSession, () => {
  persistModelCheckRunSessionState()
}, { deep: true })

export function appendModelCheckTerminalLine(level: ModelCheckTerminalLine['level'], text: string): void {
  modelCheckRunSession.terminalVisible = true
  modelCheckRunSession.terminalLines.push({
    id: ++terminalLineId,
    time: formatClockTime(new Date()),
    level,
    text
  })
}

export function resetModelCheckTerminal(): void {
  terminalLineId = 0
  modelCheckRunSession.terminalVisible = true
  modelCheckRunSession.terminalLines = []
}

export async function startModelCheckRunSession(input: {
  commandText: string
  run: (signal: AbortSignal, onProgress: (event: ModelCheckProgressEvent) => void) => Promise<ModelCheckRunDetail>
  onProgress: (event: ModelCheckProgressEvent) => void
}): Promise<ModelCheckRunDetail> {
  if (modelCheckRunSession.submitting) {
    throw new ModelCheckSessionBusyError()
  }
  const runSerial = ++activeRunSerial
  const controller = new AbortController()
  activeAbortController = controller
  modelCheckRunSession.submitting = true
  modelCheckRunSession.detached = false
  modelCheckRunSession.stopRequested = false
  modelCheckRunSession.currentRun = undefined
  resetModelCheckTerminal()
  appendModelCheckTerminalLine('info', input.commandText)
  appendModelCheckTerminalLine('muted', '已连接系统 API，等待后端返回检测进度流')

  try {
    const detail = await input.run(controller.signal, input.onProgress)
    if (activeRunSerial === runSerial) {
      modelCheckRunSession.currentRun = detail
    }
    return detail
  } finally {
    if (activeRunSerial === runSerial) {
      activeAbortController = undefined
      modelCheckRunSession.submitting = false
      modelCheckRunSession.detached = false
      modelCheckRunSession.stopRequested = false
    }
  }
}

export async function stopModelCheckRunSession(input?: {
  appendLog?: boolean
  stopRequest?: () => Promise<{ stopped: boolean }>
}): Promise<void> {
  if (!modelCheckRunSession.submitting) return
  const hasLocalStream = Boolean(activeAbortController)
  modelCheckRunSession.stopRequested = true
  if (input?.appendLog !== false) {
    appendModelCheckTerminalLine('warning', '已请求停止当前检测')
  }
  try {
    const result = await input?.stopRequest?.()
    if (result?.stopped && !hasLocalStream) {
      modelCheckRunSession.submitting = false
      modelCheckRunSession.detached = false
      modelCheckRunSession.stopRequested = false
      appendModelCheckTerminalLine('warning', '停止请求已提交，后端任务正在收尾')
      return
    }
    if (result && !result.stopped && activeAbortController && !activeAbortController.signal.aborted) {
      activeAbortController.abort()
    }
    if (result && !result.stopped && !hasLocalStream) {
      modelCheckRunSession.submitting = false
      modelCheckRunSession.detached = false
      modelCheckRunSession.stopRequested = false
    }
  } catch (error) {
    appendModelCheckTerminalLine('warning', `停止请求未确认，已关闭本地检测连接：${extractApiErrorMessage(error, '停止模型检测失败')}`)
    if (activeAbortController && !activeAbortController.signal.aborted) {
      activeAbortController.abort()
    }
    if (!hasLocalStream) {
      modelCheckRunSession.submitting = false
      modelCheckRunSession.detached = false
      modelCheckRunSession.stopRequested = false
    }
  }
}

export function reconcileModelCheckRunSessionWithActiveRun(active: ActiveModelCheckRunSummary | null): void {
  if (active) {
    modelCheckRunSession.submitting = true
    modelCheckRunSession.detached = !activeAbortController
    modelCheckRunSession.terminalVisible = true
    appendModelCheckTerminalLineOnce('muted', modelCheckTerminalRestoredText)
    return
  }
  if (!modelCheckRunSession.submitting) return
  modelCheckRunSession.submitting = false
  modelCheckRunSession.detached = false
  modelCheckRunSession.stopRequested = false
  appendModelCheckTerminalLineOnce('muted', modelCheckTerminalInactiveText)
}

export function hasActiveModelCheckRunStream(): boolean {
  return Boolean(activeAbortController)
}

function appendModelCheckTerminalLineOnce(level: ModelCheckTerminalLine['level'], text: string): void {
  if (modelCheckRunSession.terminalLines.some((line) => line.text === text)) return
  appendModelCheckTerminalLine(level, text)
}

interface ModelCheckRunSessionState {
  submitting: boolean
  detached: boolean
  terminalVisible: boolean
  terminalLines: ModelCheckTerminalLine[]
  currentRun?: ModelCheckRunDetail
  stopRequested: boolean
}

interface StoredModelCheckRunSession {
  expiresAt: number
  state: ModelCheckRunSessionState
}

function defaultModelCheckRunSessionState(): ModelCheckRunSessionState {
  return {
    submitting: false,
    detached: false,
    terminalVisible: false,
    terminalLines: [],
    currentRun: undefined,
    stopRequested: false
  }
}

function readStoredModelCheckRunSessionState(): ModelCheckRunSessionState {
  const fallback = defaultModelCheckRunSessionState()
  const storage = modelCheckSessionStorage()
  if (!storage) return fallback
  try {
    const raw = storage.getItem(modelCheckRunSessionStorageKey)
    if (!raw) return fallback
    const parsed = JSON.parse(raw) as Partial<StoredModelCheckRunSession>
    if (!parsed.expiresAt || parsed.expiresAt <= Date.now()) {
      storage.removeItem(modelCheckRunSessionStorageKey)
      return fallback
    }
    const state = sanitizeStoredModelCheckRunSessionState(parsed.state)
    return {
      ...state,
      detached: state.submitting
    }
  } catch {
    storage.removeItem(modelCheckRunSessionStorageKey)
    return fallback
  }
}

function sanitizeStoredModelCheckRunSessionState(value: unknown): ModelCheckRunSessionState {
  const fallback = defaultModelCheckRunSessionState()
  if (!value || typeof value !== 'object') return fallback
  const source = value as Partial<ModelCheckRunSessionState>
  return {
    submitting: source.submitting === true,
    detached: source.detached === true,
    terminalVisible: source.terminalVisible === true || Array.isArray(source.terminalLines) && source.terminalLines.length > 0,
    terminalLines: sanitizeTerminalLines(source.terminalLines),
    currentRun: sanitizeModelCheckRunDetail(source.currentRun),
    stopRequested: source.stopRequested === true
  }
}

function sanitizeTerminalLines(value: unknown): ModelCheckTerminalLine[] {
  if (!Array.isArray(value)) return []
  return value.map((line) => sanitizeTerminalLine(line)).filter((line): line is ModelCheckTerminalLine => Boolean(line))
}

function sanitizeTerminalLine(value: unknown): ModelCheckTerminalLine | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as Partial<ModelCheckTerminalLine>
  if (typeof source.id !== 'number' || typeof source.time !== 'string' || typeof source.text !== 'string') return undefined
  if (!isTerminalLineLevel(source.level)) return undefined
  return {
    id: source.id,
    time: source.time,
    level: source.level,
    text: source.text
  }
}

function isTerminalLineLevel(value: unknown): value is ModelCheckTerminalLine['level'] {
  return value === 'info' || value === 'success' || value === 'warning' || value === 'error' || value === 'muted'
}

function sanitizeModelCheckRunDetail(value: unknown): ModelCheckRunDetail | undefined {
  if (!value || typeof value !== 'object') return undefined
  const source = value as Partial<ModelCheckRunDetail>
  return typeof source.id === 'string' ? source as ModelCheckRunDetail : undefined
}

function persistModelCheckRunSessionState(): void {
  const storage = modelCheckSessionStorage()
  if (!storage) return
  try {
    const payload: StoredModelCheckRunSession = {
      expiresAt: Date.now() + modelCheckRunSessionStorageTtlMs,
      state: {
        submitting: modelCheckRunSession.submitting,
        detached: modelCheckRunSession.detached,
        terminalVisible: modelCheckRunSession.terminalVisible,
        terminalLines: modelCheckRunSession.terminalLines,
        currentRun: modelCheckRunSession.currentRun,
        stopRequested: modelCheckRunSession.stopRequested
      }
    }
    storage.setItem(modelCheckRunSessionStorageKey, JSON.stringify(payload))
  } catch {
    // sessionStorage 可能被浏览器策略或容量限制拒绝，运行中的检测不应因此失败。
  }
}

function modelCheckSessionStorage(): Storage | undefined {
  try {
    return typeof window === 'undefined' ? undefined : window.sessionStorage
  } catch {
    return undefined
  }
}

function maxTerminalLineId(lines: ModelCheckTerminalLine[]): number {
  return lines.reduce((maxId, line) => Math.max(maxId, line.id), 0)
}
