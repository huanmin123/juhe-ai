import { execFile } from 'node:child_process'
import { readFile } from 'node:fs/promises'
import { cpus, freemem, platform, totalmem } from 'node:os'

export interface MemoryMetricsSample {
  memoryTotalBytes: number
  memoryFreeBytes: number
  memoryUsedPercent?: number
}

export interface NetworkMetricsSample {
  networkRxBytesPerSecond?: number
  networkTxBytesPerSecond?: number
  networkRxTotalBytes?: number
  networkTxTotalBytes?: number
}

interface CpuSnapshot {
  idle: number
  total: number
}

interface NetworkCounterSnapshot {
  rxBytes: number
  txBytes: number
  sampledAtMs: number
}

let previousCpuSnapshot = cpuSnapshot()
let previousNetworkSnapshot: NetworkCounterSnapshot | undefined

export async function currentMemoryMetrics(): Promise<MemoryMetricsSample> {
  const memoryTotalBytes = totalmem()
  if (platform() === 'darwin') {
    const darwinMetrics = await readDarwinMemoryMetrics(memoryTotalBytes)
    if (darwinMetrics) return darwinMetrics
  }

  const memoryFreeBytes = freemem()
  return {
    memoryTotalBytes,
    memoryFreeBytes,
    memoryUsedPercent: percentUsed(memoryTotalBytes, memoryFreeBytes)
  }
}

export function currentCpuPercent(): number | undefined {
  const next = cpuSnapshot()
  const idleDelta = next.idle - previousCpuSnapshot.idle
  const totalDelta = next.total - previousCpuSnapshot.total
  previousCpuSnapshot = next
  if (totalDelta <= 0) return undefined
  return Math.min(100, Math.max(0, (1 - idleDelta / totalDelta) * 100))
}

export async function currentNetworkMetrics(): Promise<NetworkMetricsSample> {
  const next = await readNetworkCounterSnapshot()
  if (!next) return {}

  const previous = previousNetworkSnapshot
  previousNetworkSnapshot = next
  if (!previous) {
    return {
      networkRxTotalBytes: next.rxBytes,
      networkTxTotalBytes: next.txBytes
    }
  }

  const elapsedSeconds = (next.sampledAtMs - previous.sampledAtMs) / 1000
  if (elapsedSeconds <= 0 || next.rxBytes < previous.rxBytes || next.txBytes < previous.txBytes) {
    return {
      networkRxTotalBytes: next.rxBytes,
      networkTxTotalBytes: next.txBytes
    }
  }

  return {
    networkRxBytesPerSecond: (next.rxBytes - previous.rxBytes) / elapsedSeconds,
    networkTxBytesPerSecond: (next.txBytes - previous.txBytes) / elapsedSeconds,
    networkRxTotalBytes: next.rxBytes,
    networkTxTotalBytes: next.txBytes
  }
}

async function readDarwinMemoryMetrics(memoryTotalBytes: number): Promise<MemoryMetricsSample | undefined> {
  try {
    const stdout = await execFileText('vm_stat', [], 3000)
    return parseDarwinVmStat(stdout, memoryTotalBytes)
  } catch {
    return undefined
  }
}

function parseDarwinVmStat(output: string, memoryTotalBytes: number): MemoryMetricsSample | undefined {
  const pageSize = Number(output.match(/page size of\s+(\d+)\s+bytes/i)?.[1])
  if (!Number.isFinite(pageSize) || pageSize <= 0 || memoryTotalBytes <= 0) return undefined

  const pages = new Map<string, number>()
  for (const line of output.split('\n')) {
    const match = line.match(/^\s*"?([^":]+)"?:\s+([0-9.]+)\.?\s*$/)
    if (!match) continue
    const value = Number(match[2].replace(/\./g, ''))
    if (Number.isFinite(value)) {
      pages.set(match[1].trim().toLowerCase(), value)
    }
  }

  const anonymousPages = pages.get('anonymous pages')
  const wiredPages = pages.get('pages wired down')
  const compressorPages = pages.get('pages occupied by compressor')
  if (anonymousPages !== undefined && wiredPages !== undefined && compressorPages !== undefined) {
    const usedBytes = clampBytes((anonymousPages + wiredPages + compressorPages) * pageSize, memoryTotalBytes)
    const memoryFreeBytes = memoryTotalBytes - usedBytes
    return {
      memoryTotalBytes,
      memoryFreeBytes,
      memoryUsedPercent: percentUsed(memoryTotalBytes, memoryFreeBytes)
    }
  }

  const freePages = pages.get('pages free')
  const inactivePages = pages.get('pages inactive')
  const speculativePages = pages.get('pages speculative')
  if (freePages === undefined || inactivePages === undefined || speculativePages === undefined) return undefined

  const memoryFreeBytes = clampBytes((freePages + inactivePages + speculativePages) * pageSize, memoryTotalBytes)
  return {
    memoryTotalBytes,
    memoryFreeBytes,
    memoryUsedPercent: percentUsed(memoryTotalBytes, memoryFreeBytes)
  }
}

function cpuSnapshot(): CpuSnapshot {
  let idle = 0
  let total = 0
  for (const cpu of cpus()) {
    idle += cpu.times.idle
    total += Object.values(cpu.times).reduce((sum, value) => sum + value, 0)
  }
  return { idle, total }
}

async function readNetworkCounterSnapshot(): Promise<NetworkCounterSnapshot | undefined> {
  const currentPlatform = platform()
  const counters = currentPlatform === 'win32'
    ? await readWindowsNetworkCounters()
    : currentPlatform === 'darwin'
      ? await readDarwinNetworkCounters()
      : await readProcNetworkCounters()
  if (!counters) return undefined
  return { ...counters, sampledAtMs: Date.now() }
}

async function readProcNetworkCounters(): Promise<{ rxBytes: number; txBytes: number } | undefined> {
  const path = '/proc/net/dev'
  try {
    const lines = (await readFile(path, 'utf8')).split('\n').slice(2)
    let rxBytes = 0
    let txBytes = 0
    for (const line of lines) {
      const [ifacePart, dataPart] = line.split(':')
      if (!ifacePart || !dataPart) continue
      if (ifacePart.trim() === 'lo') continue
      const fields = dataPart.trim().split(/\s+/).map((value) => Number(value))
      if (fields.length < 16 || !Number.isFinite(fields[0]) || !Number.isFinite(fields[8])) continue
      rxBytes += fields[0]
      txBytes += fields[8]
    }
    return rxBytes > 0 || txBytes > 0 ? { rxBytes, txBytes } : undefined
  } catch {
    return undefined
  }
}

async function readWindowsNetworkCounters(): Promise<{ rxBytes: number; txBytes: number } | undefined> {
  const command = `
$adapters = Get-NetAdapter -ErrorAction SilentlyContinue | Where-Object { $_.Status -eq 'Up' -and $_.Name -notmatch 'Loopback' }
$stats = foreach ($adapter in $adapters) { Get-NetAdapterStatistics -Name $adapter.Name -ErrorAction SilentlyContinue }
$rx = ($stats | Measure-Object -Property ReceivedBytes -Sum).Sum
$tx = ($stats | Measure-Object -Property SentBytes -Sum).Sum
if ($null -eq $rx) { $rx = 0 }
if ($null -eq $tx) { $tx = 0 }
[pscustomobject]@{ rxBytes = [double]$rx; txBytes = [double]$tx } | ConvertTo-Json -Compress
`.trim()
  try {
    const stdout = await execFileText('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command', command], 5000)
    const parsed = JSON.parse(stdout) as { rxBytes?: unknown; txBytes?: unknown }
    const rxBytes = numberValue(parsed.rxBytes)
    const txBytes = numberValue(parsed.txBytes)
    return rxBytes !== undefined && txBytes !== undefined ? { rxBytes, txBytes } : undefined
  } catch {
    return undefined
  }
}

async function readDarwinNetworkCounters(): Promise<{ rxBytes: number; txBytes: number } | undefined> {
  try {
    return parseDarwinNetworkCounters(await execFileText('netstat', ['-ibn'], 5000))
  } catch {
    return undefined
  }
}

function parseDarwinNetworkCounters(output: string): { rxBytes: number; txBytes: number } | undefined {
  let rxBytes = 0
  let txBytes = 0

  for (const line of output.split('\n')) {
    const fields = line.trim().split(/\s+/)
    if (fields.length < 10 || fields[2] === undefined || !fields[2].startsWith('<Link#')) continue

    const interfaceName = fields[0]
    if (!interfaceName || interfaceName === 'lo0' || interfaceName.endsWith('*')) continue

    const rxValue = numberValue(fields[6])
    const txValue = numberValue(fields[9])
    if (rxValue === undefined || txValue === undefined) continue

    rxBytes += rxValue
    txBytes += txValue
  }

  return rxBytes > 0 || txBytes > 0 ? { rxBytes, txBytes } : undefined
}

function execFileText(file: string, args: string[], timeout: number): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(file, args, { timeout, windowsHide: true }, (error, stdout) => {
      if (error) {
        reject(error)
        return
      }
      resolve(stdout.toString())
    })
  })
}

function percentUsed(memoryTotalBytes: number, memoryFreeBytes: number): number | undefined {
  return memoryTotalBytes > 0 ? ((memoryTotalBytes - memoryFreeBytes) / memoryTotalBytes) * 100 : undefined
}

function clampBytes(value: number, max: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.min(Math.max(Math.round(value), 0), max)
}

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? number : undefined
}
