import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { basename, resolve } from 'node:path'

import {
  listWindowsProcessIdentities,
  sameProcessIdentity,
  type TrackedProcessIdentity
} from './chat-long-session-process-tree.js'
import { pickChildProcessBaseEnv } from './chat-long-session-runtime.js'

export interface SafeBusyCleanupDiagnostic {
  attempt: number
  target: {
    basename: string
    pathHash: string
  }
  locator: 'restart-manager' | 'unavailable' | 'not-windows'
  holders: Array<{
    pid: number
    commandCategory: string
    runIdentity: 'tracked' | 'untracked'
  }>
}

export function buildSafeBusyCleanupDiagnostic(input: {
  attempt: number
  targetPath: string
  holders: readonly TrackedProcessIdentity[]
  tracked: readonly TrackedProcessIdentity[]
  locator?: SafeBusyCleanupDiagnostic['locator']
}): SafeBusyCleanupDiagnostic {
  const trackedByPid = new Map(input.tracked.map((identity) => [identity.pid, identity]))
  return {
    attempt: input.attempt,
    target: {
      basename: basename(input.targetPath),
      pathHash: createHash('sha256').update(resolve(input.targetPath).toLowerCase()).digest('hex').slice(0, 12)
    },
    locator: input.locator ?? 'restart-manager',
    holders: input.holders.map((holder) => ({
      pid: holder.pid,
      commandCategory: classifyProcessCommand(holder.commandLine),
      runIdentity: sameProcessIdentity(trackedByPid.get(holder.pid) ?? emptyIdentity(), holder) ? 'tracked' : 'untracked'
    }))
  }
}

export async function collectWindowsBusyCleanupDiagnostic(input: {
  attempt: number
  targetPath: string
  tracked: readonly TrackedProcessIdentity[]
}): Promise<SafeBusyCleanupDiagnostic> {
  if (process.platform !== 'win32') {
    return buildSafeBusyCleanupDiagnostic({ ...input, holders: [], locator: 'not-windows' })
  }
  try {
    const holderPids = await locateWindowsRestartManagerHolderPids(input.targetPath)
    const identities = await listWindowsProcessIdentities()
    const holders = identities.filter((identity) => holderPids.includes(identity.pid))
    return buildSafeBusyCleanupDiagnostic({ ...input, holders, locator: 'restart-manager' })
  } catch {
    return buildSafeBusyCleanupDiagnostic({ ...input, holders: [], locator: 'unavailable' })
  }
}

export function busyCleanupTargetPath(error: unknown, fallbackPath: string): string {
  if (error && typeof error === 'object') {
    for (const key of ['path', 'dest'] as const) {
      const value = (error as Record<string, unknown>)[key]
      if (typeof value === 'string' && value.trim()) return value
    }
  }
  return fallbackPath
}

export function classifyProcessCommand(commandLine: string): string {
  const normalized = commandLine.toLowerCase()
  if (normalized.includes('sqlite-read-worker')) return 'sqlite-read-worker'
  if (normalized.includes('codex-context-state-writer-worker')) return 'codex-context-writer'
  if (normalized.includes('usage-record-writer-worker')) return 'usage-writer'
  if (normalized.includes('db-service')) return 'db-service'
  if (/(?:^|[\\/])server\.(?:ts|js)(?:\s|$)/.test(normalized)) return 'server'
  if (normalized.includes('pnpm') || normalized.includes('npm-cli')) return 'package-shell'
  if (normalized.includes('node.exe') || /(?:^|[\\/])node(?:\s|$)/.test(normalized)) return 'node'
  return 'other'
}

function emptyIdentity(): TrackedProcessIdentity {
  return { pid: -1, parentPid: -1, creationTime: '', commandLine: '' }
}

function locateWindowsRestartManagerHolderPids(targetPath: string): Promise<number[]> {
  const script = String.raw`
$ErrorActionPreference = 'Stop'
$source = @'
using System;
using System.Runtime.InteropServices;
public static class JuheRestartManager {
  [StructLayout(LayoutKind.Sequential)] public struct RM_UNIQUE_PROCESS { public int dwProcessId; public System.Runtime.InteropServices.ComTypes.FILETIME ProcessStartTime; }
  public const int CCH_RM_MAX_APP_NAME = 255;
  public const int CCH_RM_MAX_SVC_NAME = 63;
  [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)] public struct RM_PROCESS_INFO {
    public RM_UNIQUE_PROCESS Process;
    [MarshalAs(UnmanagedType.ByValTStr, SizeConst = CCH_RM_MAX_APP_NAME + 1)] public string strAppName;
    [MarshalAs(UnmanagedType.ByValTStr, SizeConst = CCH_RM_MAX_SVC_NAME + 1)] public string strServiceShortName;
    public uint ApplicationType; public uint AppStatus; public uint TSSessionId;
    [MarshalAs(UnmanagedType.Bool)] public bool bRestartable;
  }
  [DllImport("rstrtmgr.dll", CharSet = CharSet.Unicode)] public static extern int RmStartSession(out uint handle, int flags, string key);
  [DllImport("rstrtmgr.dll", CharSet = CharSet.Unicode)] public static extern int RmRegisterResources(uint handle, uint fileCount, string[] files, uint appCount, RM_UNIQUE_PROCESS[] apps, uint serviceCount, string[] services);
  [DllImport("rstrtmgr.dll")] public static extern int RmGetList(uint handle, out uint needed, ref uint count, [In, Out] RM_PROCESS_INFO[] affected, ref uint reasons);
  [DllImport("rstrtmgr.dll")] public static extern int RmEndSession(uint handle);
}
'@
Add-Type -TypeDefinition $source
$handle = [uint32]0
$key = [guid]::NewGuid().ToString('N')
$result = [JuheRestartManager]::RmStartSession([ref]$handle, 0, $key)
if ($result -ne 0) { throw "restart_manager_start_$result" }
try {
  $files = [string[]]@($env:JUHE_AI_LOCK_TARGET)
  $result = [JuheRestartManager]::RmRegisterResources($handle, 1, $files, 0, $null, 0, $null)
  if ($result -ne 0) { throw "restart_manager_register_$result" }
  $needed = [uint32]0; $count = [uint32]0; $reasons = [uint32]0
  $result = [JuheRestartManager]::RmGetList($handle, [ref]$needed, [ref]$count, $null, [ref]$reasons)
  if ($result -eq 234) {
    $items = New-Object JuheRestartManager+RM_PROCESS_INFO[] $needed
    $count = $needed
    $result = [JuheRestartManager]::RmGetList($handle, [ref]$needed, [ref]$count, $items, [ref]$reasons)
    if ($result -ne 0) { throw "restart_manager_list_$result" }
    @($items | Select-Object -First $count | ForEach-Object { [int]$_.Process.dwProcessId }) | ConvertTo-Json -Compress
  } elseif ($result -eq 0) { '[]' } else { throw "restart_manager_probe_$result" }
} finally { [void][JuheRestartManager]::RmEndSession($handle) }
`
  return runBoundedDiagnosticProcess('pwsh', ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', script], {
    timeoutMs: 2_000,
    maxStdoutBytes: 64 * 1024,
    maxStderrBytes: 4 * 1024,
    env: { ...pickChildProcessBaseEnv(process.env), JUHE_AI_LOCK_TARGET: targetPath }
  }).then((stdout) => {
    const parsed = JSON.parse(stdout.trim() || '[]') as unknown
    const values = Array.isArray(parsed) ? parsed : [parsed]
    return values.map(Number).filter((pid) => Number.isInteger(pid) && pid > 0)
  })
}

export function runBoundedDiagnosticProcess(
  command: string,
  args: readonly string[],
  options: {
    timeoutMs: number
    maxStdoutBytes: number
    maxStderrBytes: number
    env?: NodeJS.ProcessEnv
    scheduleTimeout?: (callback: () => void, delayMs: number) => () => void
    onStdoutChunk?: (text: string) => void
  }
): Promise<string> {
  return new Promise<string>((resolveRun, rejectRun) => {
    const child = spawn(command, [...args], {
      shell: false,
      windowsHide: true,
      stdio: ['ignore', 'pipe', 'pipe'],
      ...(options.env ? { env: options.env } : {})
    })
    let stdout = ''
    let stdoutBytes = 0
    let stderrBytes = 0
    let settled = false
    let cancelTimeout = (): void => undefined
    const settleFailure = (error: Error): void => {
      if (settled) return
      settled = true
      cancelTimeout()
      void terminateDiagnosticProcessTree(child.pid, child).finally(() => rejectRun(error))
    }
    cancelTimeout = (options.scheduleTimeout ?? scheduleNativeTimeout)(
      () => settleFailure(new Error('chat_long_session_cleanup_diagnostic_timeout')),
      options.timeoutMs
    )
    child.stdout.on('data', (chunk: Buffer) => {
      stdoutBytes += chunk.byteLength
      if (stdoutBytes > options.maxStdoutBytes) { settleFailure(new Error('chat_long_session_cleanup_diagnostic_stdout_too_large')); return }
      const text = chunk.toString('utf8')
      stdout += text
      options.onStdoutChunk?.(text)
    })
    child.stderr.on('data', (chunk: Buffer) => {
      stderrBytes += chunk.byteLength
      if (stderrBytes > options.maxStderrBytes) settleFailure(new Error('chat_long_session_cleanup_diagnostic_stderr_too_large'))
    })
    child.once('error', () => settleFailure(new Error('chat_long_session_cleanup_diagnostic_spawn_failed')))
    child.once('close', (code) => {
      if (settled) return
      settled = true
      cancelTimeout()
      if (code === 0) resolveRun(stdout)
      else rejectRun(new Error(`chat_long_session_cleanup_diagnostic_exit_${code ?? 'unknown'}`))
    })
  })
}

function scheduleNativeTimeout(callback: () => void, delayMs: number): () => void {
  const timeout = setTimeout(callback, delayMs)
  return () => clearTimeout(timeout)
}

async function terminateDiagnosticProcessTree(pid: number | undefined, child: ReturnType<typeof spawn>): Promise<void> {
  if (!pid || child.exitCode !== null) return
  if (process.platform === 'win32') {
    await new Promise<void>((resolveKill) => {
      const killer = spawn('taskkill', ['/pid', String(pid), '/t', '/f'], { shell: false, windowsHide: true, stdio: 'ignore' })
      let finished = false
      const finish = (): void => {
        if (finished) return
        finished = true
        clearTimeout(timeout)
        resolveKill()
      }
      const timeout = setTimeout(() => { killer.kill('SIGKILL'); finish() }, 1_000)
      killer.once('error', finish)
      killer.once('close', finish)
    })
  }
  if (child.exitCode === null) {
    try { child.kill('SIGKILL') } catch { }
  }
  if (child.exitCode !== null) return
  await new Promise<void>((resolveClose) => {
    const timeout = setTimeout(resolveClose, 500)
    child.once('close', () => { clearTimeout(timeout); resolveClose() })
  })
}
