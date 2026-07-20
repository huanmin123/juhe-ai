import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { LogWriterWorkerClient } from '../../shared/logging/log-writer-worker-client.js'

const directory = join(tmpdir(), `juhe-ai-log-writer-${process.pid}-${Date.now()}`)
mkdirSync(directory, { recursive: true })
try {
  const indexedChunks: Buffer[] = []
  const client = new LogWriterWorkerClient({
    directory,
    fileName: 'worker-test.log',
    fileEnabled: true,
    consoleEnabled: false,
    maxFileBytes: 1024,
    retentionDays: 1,
    maxFiles: 3,
    onLine: (chunk) => indexedChunks.push(chunk)
  })
  await write(client, Buffer.from('{"event":"one"}\n'))
  await write(client, Buffer.from('{"event":"two"}\n'))
  await writeBatch(client, [Buffer.from('{"event":"three"}\n'), Buffer.from('{"event":"four"}\n')])
  await client.close()
  const content = readFileSync(join(directory, 'worker-test.log'), 'utf8')
  assert.match(content, /"event":"one"/)
  assert.match(content, /"event":"two"/)
  assert.match(content, /"event":"four"/)
  assert.equal(indexedChunks.length, 4)
  console.log('日志 writer worker 回归通过')
} finally {
  rmSync(directory, { recursive: true, force: true })
}

function write(client: LogWriterWorkerClient, chunk: Buffer): Promise<void> {
  return new Promise((resolve, reject) => client.write(chunk, (error) => error ? reject(error) : resolve()))
}

function writeBatch(client: LogWriterWorkerClient, chunks: Buffer[]): Promise<void> {
  return new Promise((resolve, reject) => client.writeBatch(chunks, (error) => error ? reject(error) : resolve()))
}
