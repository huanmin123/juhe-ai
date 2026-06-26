import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { createReadStream, createWriteStream } from 'node:fs'
import { lstat, mkdir, mkdtemp, opendir, rm, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { Transform } from 'node:stream'
import { pipeline } from 'node:stream/promises'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import type { GatewayRuntimeRequest } from '../gateway/request/pre-auth.js'
import {
  ensureOpenAICompatibleFileObjectParent,
  mediaTypeFromFilename,
  newOpenAICompatibleFileId,
  removeOpenAICompatibleFileObject,
  storageKeyForOpenAICompatibleFile
} from '../openai-compatible-files/file-storage.js'
import type {
  OpenAIToAnthropicCodeInterpreterExecutionResult,
  OpenAIToAnthropicCodeInterpreterExecutor,
  OpenAIToAnthropicCodeInterpreterRuntimeInput
} from '../providers/drivers/_shared/openai-anthropic-bridge.js'

interface CodeInterpreterGatewayScope {
  systemAccountId: string
  apiKeyId: string
}

export function openAICompatibleCodeInterpreterExecutorForGatewayRequest(req: Request): OpenAIToAnthropicCodeInterpreterExecutor | undefined {
  if (runtimeConfig.hostedToolRuntimes.codeInterpreter !== 'local_runtime') return undefined
  if (!runtimeConfig.codeInterpreter.pythonCommand.trim()) return undefined
  const scope = codeInterpreterGatewayScope(req)
  return {
    execute(input) {
      return executeOpenAICompatibleCodeInterpreter(input, scope)
    }
  }
}

async function executeOpenAICompatibleCodeInterpreter(
  input: OpenAIToAnthropicCodeInterpreterRuntimeInput,
  scope: CodeInterpreterGatewayScope | undefined
): Promise<OpenAIToAnthropicCodeInterpreterExecutionResult> {
  const codeBytes = Buffer.byteLength(input.code, 'utf8')
  if (codeBytes > runtimeConfig.codeInterpreter.maxCodeBytes) {
    return {
      stdout: '',
      stderr: `Code interpreter input exceeds gateway limit (${codeBytes} > ${runtimeConfig.codeInterpreter.maxCodeBytes} bytes).`,
      exitCode: 1,
      metadata: {
        error_code: 'code_too_large',
        code_bytes: codeBytes,
        max_code_bytes: runtimeConfig.codeInterpreter.maxCodeBytes
      }
    }
  }

  await mkdir(runtimeConfig.codeInterpreter.tempRoot, { recursive: true })
  const workDir = await mkdtemp(join(runtimeConfig.codeInterpreter.tempRoot, 'ci-'))
  try {
    const codePath = join(workDir, 'input.py')
    const runnerPath = join(workDir, 'runner.py')
    await writeFile(codePath, input.code, 'utf8')
    await writeFile(runnerPath, codeInterpreterRunnerSource(), 'utf8')
    const executionResult = await spawnPythonCodeInterpreter(runnerPath, codePath, workDir, input.signal)
    const artifactResult = await collectCodeInterpreterArtifacts(workDir, scope, input.containerId)
    return {
      ...executionResult,
      ...artifactResult,
      metadata: {
        ...(executionResult.metadata ?? {}),
        ...(artifactResult.metadata ?? {})
      }
    }
  } finally {
    if (runtimeConfig.codeInterpreter.cleanupTempDirectory) {
      await rm(workDir, { recursive: true, force: true })
    }
  }
}

function spawnPythonCodeInterpreter(
  runnerPath: string,
  codePath: string,
  workDir: string,
  signal?: AbortSignal
): Promise<OpenAIToAnthropicCodeInterpreterExecutionResult> {
  return new Promise((resolve) => {
    const stdout: string[] = []
    const stderr: string[] = []
    let remainingOutputBytes = runtimeConfig.codeInterpreter.maxOutputBytes
    let outputTruncated = false
    let timedOut = false
    let aborted = false
    let settled = false

    const child = spawn(
      runtimeConfig.codeInterpreter.pythonCommand,
      ['-I', '-B', runnerPath, codePath],
      {
        cwd: workDir,
        env: codeInterpreterProcessEnv(),
        stdio: ['ignore', 'pipe', 'pipe'],
        windowsHide: true
      }
    )

    const timeout = setTimeout(() => {
      timedOut = true
      child.kill()
    }, runtimeConfig.codeInterpreter.timeoutMs)

    const abortListener = () => {
      aborted = true
      child.kill()
    }
    signal?.addEventListener('abort', abortListener, { once: true })

    child.stdout?.on('data', (chunk: Buffer) => {
      appendLimitedOutput(stdout, chunk, {
        remainingOutputBytes: () => remainingOutputBytes,
        setRemainingOutputBytes: (value) => { remainingOutputBytes = value },
        markTruncated: () => {
          outputTruncated = true
          child.kill()
        }
      })
    })
    child.stderr?.on('data', (chunk: Buffer) => {
      appendLimitedOutput(stderr, chunk, {
        remainingOutputBytes: () => remainingOutputBytes,
        setRemainingOutputBytes: (value) => { remainingOutputBytes = value },
        markTruncated: () => {
          outputTruncated = true
          child.kill()
        }
      })
    })

    child.on('error', (error) => {
      if (settled) return
      settled = true
      clearTimeout(timeout)
      signal?.removeEventListener('abort', abortListener)
      resolve({
        stdout: stdout.join(''),
        stderr: `Code interpreter python process failed: ${error.message}`,
        exitCode: 1,
        metadata: { error_code: 'python_spawn_failed' }
      })
    })
    child.on('close', (code) => {
      if (settled) return
      settled = true
      clearTimeout(timeout)
      signal?.removeEventListener('abort', abortListener)
      resolve({
        stdout: stdout.join(''),
        stderr: stderr.join(''),
        exitCode: typeof code === 'number' ? code : undefined,
        timedOut,
        outputTruncated,
        metadata: aborted ? { aborted: true } : undefined
      })
    })
  })
}

interface CodeInterpreterArtifactCollectionResult {
  artifacts: NonNullable<OpenAIToAnthropicCodeInterpreterExecutionResult['artifacts']>
  artifactsOmittedCount?: number
  artifactsTotalBytes?: number
  metadata?: Record<string, unknown>
}

async function collectCodeInterpreterArtifacts(
  workDir: string,
  scope: CodeInterpreterGatewayScope | undefined,
  containerId: string | undefined
): Promise<CodeInterpreterArtifactCollectionResult> {
  const artifacts: NonNullable<OpenAIToAnthropicCodeInterpreterExecutionResult['artifacts']> = []
  let artifactsOmittedCount = 0
  let artifactsTotalBytes = 0
  let scannedEntries = 0
  let scanFailed = false
  try {
    await collectCodeInterpreterArtifactsFromDirectory(workDir, '', scope, containerId, {
      artifacts,
      incrementOmittedCount: () => { artifactsOmittedCount += 1 },
      incrementTotalBytes: (bytes) => { artifactsTotalBytes += bytes },
      incrementScannedEntries: () => { scannedEntries += 1 }
    })
  } catch {
    scanFailed = true
  }
  const metadata: Record<string, unknown> = {
    artifacts_scanned_entries: scannedEntries
  }
  if (scanFailed) metadata.artifacts_scan_failed = true
  return {
    artifacts,
    artifactsOmittedCount,
    artifactsTotalBytes,
    metadata
  }
}

async function collectCodeInterpreterArtifactsFromDirectory(
  workDir: string,
  relativeDir: string,
  scope: CodeInterpreterGatewayScope | undefined,
  containerId: string | undefined,
  collector: {
    artifacts: NonNullable<OpenAIToAnthropicCodeInterpreterExecutionResult['artifacts']>
    incrementOmittedCount: () => void
    incrementTotalBytes: (bytes: number) => void
    incrementScannedEntries: () => void
  }
): Promise<void> {
  const absoluteDir = relativeDir ? join(workDir, relativeDir) : workDir
  const directory = await opendir(absoluteDir)
  for await (const entry of directory) {
    collector.incrementScannedEntries()
    const entryRelativePath = normalizeCodeInterpreterArtifactPath(relativeDir ? join(relativeDir, entry.name) : entry.name)
    if (!relativeDir && (entry.name === 'input.py' || entry.name === 'runner.py')) continue
    if (entry.isSymbolicLink()) {
      collector.incrementOmittedCount()
      continue
    }
    if (entry.isDirectory()) {
      await collectCodeInterpreterArtifactsFromDirectory(workDir, entryRelativePath, scope, containerId, collector)
      continue
    }
    if (!entry.isFile()) {
      collector.incrementOmittedCount()
      continue
    }
    let stats: Awaited<ReturnType<typeof lstat>>
    try {
      stats = await lstat(join(absoluteDir, entry.name))
    } catch {
      collector.incrementOmittedCount()
      continue
    }
    collector.incrementTotalBytes(stats.size)
    collector.artifacts.push(await codeInterpreterArtifactFromFile({
      absolutePath: join(absoluteDir, entry.name),
      filename: entryRelativePath,
      bytes: stats.size,
      scope,
      containerId
    }))
    if (collector.artifacts.length > runtimeConfig.codeInterpreter.maxArtifactCount) {
      collector.artifacts.pop()
      collector.incrementOmittedCount()
    }
  }
}

async function codeInterpreterArtifactFromFile(input: {
  absolutePath: string
  filename: string
  bytes: number
  scope: CodeInterpreterGatewayScope | undefined
  containerId: string | undefined
}): Promise<NonNullable<OpenAIToAnthropicCodeInterpreterExecutionResult['artifacts']>[number]> {
  const mediaType = mediaTypeFromFilename(input.filename)
  if (input.bytes > runtimeConfig.codeInterpreter.maxArtifactBytes) {
    return {
      filename: input.filename,
      bytes: input.bytes,
      mediaType,
      containerId: input.containerId,
      contentOmitted: true,
      omitReason: 'file_too_large'
    }
  }
  if (!input.scope) {
    return {
      filename: input.filename,
      bytes: input.bytes,
      mediaType,
      containerId: input.containerId,
      contentOmitted: true,
      omitReason: 'missing_gateway_scope'
    }
  }
  try {
    const persisted = await persistCodeInterpreterArtifactFile(input.absolutePath, {
      filename: input.filename,
      bytes: input.bytes,
      mediaType,
      containerId: input.containerId,
      scope: input.scope
    })
    return {
      filename: input.filename,
      bytes: input.bytes,
      fileId: persisted.fileId,
      downloadPath: `/v1/files/${persisted.fileId}/content`,
      containerId: input.containerId,
      containerDownloadPath: input.containerId
        ? `/v1/containers/${input.containerId}/files/${persisted.fileId}/content`
        : undefined,
      mediaType
    }
  } catch {
    return {
      filename: input.filename,
      bytes: input.bytes,
      mediaType,
      containerId: input.containerId,
      contentOmitted: true,
      omitReason: 'persist_failed'
    }
  }
}

async function persistCodeInterpreterArtifactFile(
  sourcePath: string,
  input: {
    filename: string
    bytes: number
    mediaType?: string
    containerId?: string
    scope: CodeInterpreterGatewayScope
  }
): Promise<{ fileId: string }> {
  const fileId = newOpenAICompatibleFileId()
  const storageKey = storageKeyForOpenAICompatibleFile(fileId)
  const targetPath = ensureOpenAICompatibleFileObjectParent(storageKey)
  const hash = createHash('sha256')
  const hashStream = new Transform({
    transform(chunk: Buffer, _encoding, callback) {
      hash.update(chunk)
      callback(null, chunk)
    }
  })
  try {
    await pipeline(createReadStream(sourcePath), hashStream, createWriteStream(targetPath))
    await requestDbService({
      type: 'create_openai_compatible_file',
      input: {
        id: fileId,
        systemAccountId: input.scope.systemAccountId,
        apiKeyId: input.scope.apiKeyId,
        purpose: 'code_interpreter_output',
        containerId: input.containerId,
        filename: input.filename,
        bytes: input.bytes,
        mediaType: input.mediaType,
        storageKey,
        sha256: hash.digest('hex')
      }
    }, { timeoutMs: 10_000 })
    return { fileId }
  } catch (error) {
    await removeOpenAICompatibleFileObject(storageKey).catch(() => undefined)
    throw error
  }
}

function codeInterpreterGatewayScope(req: Request): CodeInterpreterGatewayScope | undefined {
  const apiKey = (req as GatewayRuntimeRequest).gatewayRuntime?.apiKey
  if (!apiKey) return undefined
  return {
    systemAccountId: apiKey.system_account_id,
    apiKeyId: apiKey.id
  }
}

function normalizeCodeInterpreterArtifactPath(value: string): string {
  return value.replace(/\\/g, '/').replace(/\/{2,}/g, '/').replace(/^\.\//, '').replace(/[\u0000-\u001F\u007F]/g, '_').slice(0, 512) || 'artifact'
}

function appendLimitedOutput(
  output: string[],
  chunk: Buffer,
  state: {
    remainingOutputBytes: () => number
    setRemainingOutputBytes: (value: number) => void
    markTruncated: () => void
  }
): void {
  const remaining = state.remainingOutputBytes()
  if (remaining <= 0) {
    state.markTruncated()
    return
  }
  if (chunk.byteLength <= remaining) {
    output.push(chunk.toString('utf8'))
    state.setRemainingOutputBytes(remaining - chunk.byteLength)
    return
  }
  output.push(chunk.subarray(0, remaining).toString('utf8'))
  state.setRemainingOutputBytes(0)
  state.markTruncated()
}

function codeInterpreterProcessEnv(): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {
    PYTHONIOENCODING: 'utf-8',
    PYTHONUTF8: '1',
    NO_PROXY: '*'
  }
  for (const key of ['PATH', 'Path', 'SystemRoot', 'WINDIR', 'TEMP', 'TMP', 'PATHEXT']) {
    const value = process.env[key]
    if (value) env[key] = value
  }
  return env
}

function codeInterpreterRunnerSource(): string {
  return String.raw`
import os
import socket
import subprocess
import sys
import traceback

def _blocked(*args, **kwargs):
    raise RuntimeError("Network and subprocess access are disabled in this code interpreter runtime")

socket.socket = _blocked
socket.create_connection = _blocked
subprocess.Popen = _blocked
subprocess.run = _blocked
subprocess.call = _blocked
subprocess.check_call = _blocked
subprocess.check_output = _blocked
os.system = _blocked
os.popen = _blocked

code_path = sys.argv[1]
globals_dict = {"__name__": "__main__", "__file__": code_path}

try:
    with open(code_path, "r", encoding="utf-8") as handle:
        source = handle.read()
    exec(compile(source, code_path, "exec"), globals_dict)
except SystemExit:
    raise
except BaseException:
    traceback.print_exc()
    sys.exit(1)
`
}
