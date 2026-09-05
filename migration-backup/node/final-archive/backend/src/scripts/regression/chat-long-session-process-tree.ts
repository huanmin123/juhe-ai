import { spawn } from 'node:child_process'
import net from 'node:net'

export interface TrackedProcessIdentity {
  pid: number
  parentPid: number
  creationTime: string
  commandLine: string
}

export async function listWindowsProcessIdentities(): Promise<TrackedProcessIdentity[]> {
  if (process.platform !== 'win32') return []
  const script = [
    "$ErrorActionPreference = 'Stop'",
    '@(Get-CimInstance Win32_Process | ForEach-Object {',
    "  [pscustomobject]@{ pid = [int]$_.ProcessId; parentPid = [int]$_.ParentProcessId; creationTime = if ($_.CreationDate) { $_.CreationDate.ToUniversalTime().ToString('o') } else { '' }; commandLine = [string]$_.CommandLine }",
    '}) | ConvertTo-Json -Compress'
  ].join('; ')
  const text = await runProcess('pwsh', ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', script], 8 * 1024 * 1024)
  const parsed = JSON.parse(text || '[]') as unknown
  const rows = Array.isArray(parsed) ? parsed : [parsed]
  return rows.flatMap((value) => {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return []
    const row = value as Record<string, unknown>
    const pid = Number(row.pid)
    const parentPid = Number(row.parentPid)
    if (!Number.isInteger(pid) || pid <= 0 || !Number.isInteger(parentPid)) return []
    return [{ pid, parentPid, creationTime: String(row.creationTime ?? ''), commandLine: String(row.commandLine ?? '') }]
  })
}

export async function captureWindowsProcessTree(rootPid: number): Promise<TrackedProcessIdentity[]> {
  const processes = await listWindowsProcessIdentities()
  return selectTrackedProcessTree(rootPid, processes)
}

export function selectTrackedProcessTree(
  rootPid: number,
  processes: readonly TrackedProcessIdentity[]
): TrackedProcessIdentity[] {
  const included = new Set<number>([rootPid])
  let changed = true
  while (changed) {
    changed = false
    for (const processIdentity of processes) {
      if (included.has(processIdentity.parentPid) && !included.has(processIdentity.pid)) {
        included.add(processIdentity.pid)
        changed = true
      }
    }
  }
  return processes.filter((processIdentity) => included.has(processIdentity.pid))
}

export function extendTrackedProcessTree(
  rootPid: number,
  tracked: readonly TrackedProcessIdentity[],
  processes: readonly TrackedProcessIdentity[]
): TrackedProcessIdentity[] {
  const byPid = new Map(tracked.map((identity) => [identity.pid, identity]))
  const currentByPid = new Map(processes.map((identity) => [identity.pid, identity]))
  const currentRoot = processes.find((identity) => identity.pid === rootPid)
  if (currentRoot && !byPid.has(rootPid)) byPid.set(rootPid, currentRoot)
  const activeAnchors = new Set<number>()
  for (const expected of byPid.values()) {
    const current = currentByPid.get(expected.pid)
    if (current && sameProcessIdentity(expected, current)) activeAnchors.add(expected.pid)
  }
  let changed = true
  while (changed) {
    changed = false
    for (const identity of processes) {
      if (!activeAnchors.has(identity.parentPid) || byPid.has(identity.pid)) continue
      byPid.set(identity.pid, identity)
      activeAnchors.add(identity.pid)
      changed = true
    }
  }
  return [...byPid.values()]
}

export interface WindowsProcessTreeTracker {
  sample: () => Promise<void>
  snapshot: () => TrackedProcessIdentity[]
  stop: () => Promise<TrackedProcessIdentity[]>
}

export function startWindowsProcessTreeTracker(input: {
  rootPid: number
  initial?: readonly TrackedProcessIdentity[]
  listProcesses?: () => Promise<readonly TrackedProcessIdentity[]>
  pollIntervalMs?: number
}): WindowsProcessTreeTracker {
  const listProcesses = input.listProcesses ?? listWindowsProcessIdentities
  let tracked = [...(input.initial ?? [])]
  let pending: Promise<void> | undefined
  let stopped = false
  const sample = async (): Promise<void> => {
    if (pending) return pending
    pending = listProcesses()
      .then((processes) => { tracked = extendTrackedProcessTree(input.rootPid, tracked, processes) })
      .finally(() => { pending = undefined })
    return pending
  }
  const timer = setInterval(() => { if (!stopped) void sample().catch(() => undefined) }, input.pollIntervalMs ?? 250)
  timer.unref()
  return {
    sample,
    snapshot: () => [...tracked],
    stop: async () => {
      stopped = true
      clearInterval(timer)
      if (pending) await pending
      await sample()
      return [...tracked]
    }
  }
}

export async function killWindowsProcess(pid: number): Promise<void> {
  try { process.kill(pid, 'SIGKILL') } catch (error) {
    if (!isNoSuchProcessError(error)) throw error
  }
}

export function isTcpPortListening(port: number): Promise<boolean> {
  return new Promise<boolean>((resolveListening) => {
    const socket = net.createConnection({ host: '127.0.0.1', port })
    let settled = false
    const finish = (listening: boolean): void => {
      if (settled) return
      settled = true
      socket.destroy()
      resolveListening(listening)
    }
    socket.once('connect', () => finish(true))
    socket.once('error', () => finish(false))
    socket.setTimeout(500, () => finish(false))
  })
}

interface StopTrackedWindowsProcessTreeInput {
  rootPid: number
  tracked: readonly TrackedProcessIdentity[]
  servicePort?: number
  captureTree: (rootPid: number) => Promise<readonly TrackedProcessIdentity[]>
  taskkillTree: (rootPid: number) => Promise<number>
  listCurrentProcesses: () => Promise<readonly TrackedProcessIdentity[]>
  killPid: (pid: number) => Promise<void>
  isPortListening: (port: number) => Promise<boolean>
  timeoutMs?: number
  pollIntervalMs?: number
}

export async function stopTrackedWindowsProcessTree(input: StopTrackedWindowsProcessTreeInput): Promise<void> {
  const protectedPids = new Set([process.pid, process.ppid].filter((pid) => Number.isInteger(pid) && pid > 0))
  if (protectedPids.has(input.rootPid)) throw new Error('chat_long_session_protected_process_root')
  if (input.tracked.some((identity) => protectedPids.has(identity.pid))) {
    throw new Error('chat_long_session_protected_process_in_snapshot')
  }
  let tracked = input.tracked.length ? [...input.tracked] : await input.captureTree(input.rootPid)
  if (tracked.some((identity) => protectedPids.has(identity.pid))) {
    throw new Error('chat_long_session_protected_process_in_snapshot')
  }
  const currentBeforeTaskkill = new Map((await input.listCurrentProcesses()).map((item) => [item.pid, item]))
  const expectedRoot = tracked.find((item) => item.pid === input.rootPid)
  const currentRoot = currentBeforeTaskkill.get(input.rootPid)
  let mayKillRoot = Boolean(expectedRoot && currentRoot && sameProcessIdentity(expectedRoot, currentRoot))
  if (mayKillRoot && expectedRoot) {
    const refreshed = await input.captureTree(input.rootPid)
    const refreshedRoot = refreshed.find((item) => item.pid === input.rootPid)
    if (refreshedRoot && sameProcessIdentity(expectedRoot, refreshedRoot)) tracked = mergeTrackedProcesses(tracked, refreshed)
    else mayKillRoot = false
  }
  if (mayKillRoot && expectedRoot) {
    const finalRoot = new Map((await input.listCurrentProcesses()).map((item) => [item.pid, item])).get(input.rootPid)
    if (!finalRoot || !sameProcessIdentity(expectedRoot, finalRoot)) mayKillRoot = false
  }
  let taskkillExitCode = -3
  if (mayKillRoot) {
    try { taskkillExitCode = await input.taskkillTree(input.rootPid) } catch { taskkillExitCode = -4 }
  }
  const withLateDescendants = tracked
  const killErrors: string[] = []
  const currentBeforeFallbackKill = new Map((await input.listCurrentProcesses()).map((item) => [item.pid, item]))
  for (const identity of sortDescendantsFirst(withLateDescendants)) {
    const current = currentBeforeFallbackKill.get(identity.pid)
    if (!current || !sameProcessIdentity(identity, current)) continue
    try { await input.killPid(identity.pid) } catch (error) {
      killErrors.push(error instanceof Error ? error.message : 'kill_failed')
    }
  }

  const timeoutMs = input.timeoutMs ?? 5_000
  const pollIntervalMs = input.pollIntervalMs ?? 50
  const deadline = Date.now() + timeoutMs
  while (true) {
    const current = new Map((await input.listCurrentProcesses()).map((item) => [item.pid, item]))
    const alive = withLateDescendants.filter((expected) => {
      const actual = current.get(expected.pid)
      return Boolean(actual && sameProcessIdentity(expected, actual))
    })
    const portListening = input.servicePort === undefined ? false : await input.isPortListening(input.servicePort)
    if (!alive.length && !portListening) return
    if (Date.now() >= deadline) {
      throw new Error(`chat_long_session_child_stop_failed: taskkill_${taskkillExitCode}; alive=${alive.map((item) => item.pid).join(',') || 'none'}; port=${portListening ? 'listening' : 'closed'}; kill=${killErrors.join('|') || 'none'}`)
    }
    await new Promise<void>((resolveWait) => setTimeout(resolveWait, pollIntervalMs))
  }
}

interface ChildProcessCloseLike {
  exitCode: number | null
  stdout?: { destroyed: boolean } | null
  stderr?: { destroyed: boolean } | null
  once: (event: 'close', listener: () => void) => unknown
  removeListener: (event: 'close', listener: () => void) => unknown
}

export async function waitForChildProcessClose(child: ChildProcessCloseLike, timeoutMs = 5_000): Promise<void> {
  if (child.exitCode !== null && child.stdout?.destroyed !== false && child.stderr?.destroyed !== false) return
  await new Promise<void>((resolveClose, rejectClose) => {
    const onClose = (): void => {
      clearTimeout(timeout)
      resolveClose()
    }
    const timeout = setTimeout(() => {
      child.removeListener('close', onClose)
      rejectClose(new Error('chat_long_session_child_close_timeout'))
    }, timeoutMs)
    child.once('close', onClose)
  })
}

export async function assertTrackedProcessIdentitiesStopped(
  tracked: readonly TrackedProcessIdentity[],
  options: {
    listCurrentProcesses?: () => Promise<readonly TrackedProcessIdentity[]>
    timeoutMs?: number
    pollIntervalMs?: number
  } = {}
): Promise<void> {
  if (!tracked.length) return
  const listCurrentProcesses = options.listCurrentProcesses ?? listWindowsProcessIdentities
  const deadline = Date.now() + (options.timeoutMs ?? 5_000)
  while (true) {
    const current = new Map((await listCurrentProcesses()).map((identity) => [identity.pid, identity]))
    const alive = tracked.filter((expected) => {
      const actual = current.get(expected.pid)
      return Boolean(actual && sameProcessIdentity(expected, actual))
    })
    if (!alive.length) return
    if (Date.now() >= deadline) throw new Error(`chat_long_session_tracked_processes_alive:${alive.map((identity) => identity.pid).join(',')}`)
    await new Promise<void>((resolveWait) => setTimeout(resolveWait, options.pollIntervalMs ?? 50))
  }
}

export function mergeTrackedProcesses(
  initial: readonly TrackedProcessIdentity[],
  additional: readonly TrackedProcessIdentity[]
): TrackedProcessIdentity[] {
  const byPid = new Map(initial.map((identity) => [identity.pid, identity]))
  for (const identity of additional) if (!byPid.has(identity.pid)) byPid.set(identity.pid, identity)
  return [...byPid.values()]
}

export function sameProcessIdentity(expected: TrackedProcessIdentity, current: TrackedProcessIdentity): boolean {
  return expected.pid === current.pid
    && Boolean(expected.creationTime)
    && expected.creationTime === current.creationTime
    && expected.commandLine === current.commandLine
}

function sortDescendantsFirst(tracked: readonly TrackedProcessIdentity[]): TrackedProcessIdentity[] {
  const byPid = new Map(tracked.map((identity) => [identity.pid, identity]))
  const depth = (identity: TrackedProcessIdentity): number => {
    let current = identity
    let value = 0
    const seen = new Set<number>()
    while (byPid.has(current.parentPid) && !seen.has(current.parentPid)) {
      seen.add(current.parentPid)
      current = byPid.get(current.parentPid)!
      value += 1
    }
    return value
  }
  return [...tracked].sort((left, right) => depth(right) - depth(left))
}

function runProcess(command: string, args: string[], maxOutputBytes: number): Promise<string> {
  return new Promise<string>((resolveRun, rejectRun) => {
    const child = spawn(command, args, { shell: false, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] })
    let stdout = ''
    let stderr = ''
    let outputBytes = 0
    const collect = (target: 'stdout' | 'stderr', chunk: Buffer): void => {
      outputBytes += chunk.byteLength
      if (outputBytes > maxOutputBytes) {
        child.kill('SIGKILL')
        rejectRun(new Error('chat_long_session_process_list_too_large'))
        return
      }
      if (target === 'stdout') stdout += chunk.toString('utf8')
      else stderr += chunk.toString('utf8')
    }
    child.stdout.on('data', (chunk: Buffer) => collect('stdout', chunk))
    child.stderr.on('data', (chunk: Buffer) => collect('stderr', chunk))
    child.once('error', rejectRun)
    child.once('exit', (code) => code === 0 ? resolveRun(stdout.trim()) : rejectRun(new Error(`chat_long_session_process_list_failed:${code}:${stderr.slice(-1_000)}`)))
  })
}

function isNoSuchProcessError(error: unknown): boolean {
  return Boolean(error && typeof error === 'object' && 'code' in error && (error as { code?: unknown }).code === 'ESRCH')
}
