import { strict as assert } from 'node:assert'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { createServer } from 'node:http'
import { Writable } from 'node:stream'

import { createObservedLogStreamForTest } from '../../shared/logger.js'

interface ChildResult {
  exitCode: number | null
  signal: NodeJS.Signals | null
  stderr: string
  stdoutClosed: boolean
}

class EpipeWritable extends Writable {
  _write(_chunk: Buffer, _encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    const error = Object.assign(new Error('broken pipe'), {
      code: 'EPIPE',
      syscall: 'write'
    })
    setImmediate(() => callback(error))
  }
}

async function reproduceRawStdoutEpipe(): Promise<ChildResult> {
  const childSource = `
process.on('uncaughtException', (error) => {
  process.stderr.write(JSON.stringify({ event: 'uncaught_exception', code: error && error.code, message: error && error.message }) + '\\n')
  setImmediate(() => process.exit(91))
})
process.stdout.write('READY\\n')
setInterval(() => process.stdout.write(Buffer.alloc(64 * 1024, 120)), 5)
`
  const child = spawn(process.execPath, ['-e', childSource], {
    stdio: ['ignore', 'pipe', 'pipe'],
    windowsHide: true
  })
  let stdoutClosed = false
  let stderr = ''
  child.stdout.on('data', () => {
    if (stdoutClosed) return
    stdoutClosed = true
    child.stdout.destroy()
  })
  child.stderr.on('data', (chunk: Buffer) => {
    stderr += chunk.toString('utf8')
  })
  const timeout = setTimeout(() => child.kill(), 10_000)
  const [exitCode, signal] = await once(child, 'exit') as [number | null, NodeJS.Signals | null]
  clearTimeout(timeout)
  return { exitCode, signal, stderr: stderr.trim(), stdoutClosed }
}

async function verifyObservedLogDestinationEpipe(): Promise<void> {
  const destination = new EpipeWritable()
  const uncaughtErrors: unknown[] = []
  const onUncaughtException = (error: unknown) => uncaughtErrors.push(error)
  process.on('uncaughtException', onUncaughtException)
  try {
    const observed = createObservedLogStreamForTest([{ name: 'stdout-like', stream: destination }])
    observed.stream.write(Buffer.from('epipe-log\n'))
    await new Promise<void>((resolve) => destination.once('close', () => resolve()))
    await new Promise<void>((resolve) => setImmediate(resolve))
    const stats = observed.stats()
    assert.equal(stats.errorCount, 1, '日志 destination EPIPE 必须被记录')
    assert.equal(stats.degraded, true, '日志 destination EPIPE 后必须降级')
    assert.equal(stats.lastError?.code, 'EPIPE')
    assert.equal(stats.lastError?.destination, 'stdout-like')
    assert.deepEqual(uncaughtErrors, [], '受保护日志 destination EPIPE 不得升级为全局异常')
  } finally {
    process.off('uncaughtException', onUncaughtException)
  }
}

async function verifyHttpClientDisconnect(): Promise<void> {
  const uncaughtErrors: unknown[] = []
  const responseErrors: string[] = []
  let responseClosed = 0
  const onUncaughtException = (error: unknown) => uncaughtErrors.push(error)
  process.on('uncaughtException', onUncaughtException)
  const server = createServer((_request, response) => {
    response.on('close', () => {
      responseClosed += 1
    })
    response.on('error', (error: NodeJS.ErrnoException) => {
      responseErrors.push(error.code ?? error.name)
    })
    response.writeHead(200, { 'content-type': 'text/event-stream' })
    const writer = setInterval(() => {
      try {
        response.write(Buffer.alloc(64 * 1024, 120))
      } catch (error) {
        responseErrors.push(error instanceof Error ? error.name : String(error))
      }
    }, 2)
    response.once('close', () => clearInterval(writer))
  })
  try {
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
    const address = server.address()
    assert(address && typeof address !== 'string')
    const request = (await import('node:http')).get({
      host: '127.0.0.1',
      port: address.port,
      path: '/'
    })
    request.once('response', (response) => {
      response.once('data', () => response.destroy())
    })
    await new Promise<void>((resolve) => setTimeout(resolve, 500))
    assert.equal(responseClosed, 1, '真实 HTTP 客户端断开必须关闭响应流')
    assert.deepEqual(uncaughtErrors, [], '真实 HTTP 客户端断开不得导致全局异常')
  } finally {
    server.closeAllConnections?.()
    await new Promise<void>((resolve) => server.close(() => resolve()))
    process.off('uncaughtException', onUncaughtException)
  }
}

const rawStdoutResult = await reproduceRawStdoutEpipe()
assert.equal(rawStdoutResult.stdoutClosed, true, '父进程必须关闭子进程 stdout')
assert.equal(rawStdoutResult.exitCode, 91, '裸 stdout EPIPE 必须进入子进程全局异常退出路径')
assert.match(rawStdoutResult.stderr, /"code":"EPIPE"/, '裸 stdout 退出诊断必须包含 EPIPE')
await verifyObservedLogDestinationEpipe()
await verifyHttpClientDisconnect()
process.stdout.write('EPIPE_PROVENANCE_LOCAL_REPRODUCTION_OK\n')
