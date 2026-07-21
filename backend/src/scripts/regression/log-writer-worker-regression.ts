import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { LogWriterWorkerClient } from '../../shared/logging/log-writer-worker-client.js'

const directory = join(tmpdir(), `juhe-ai-log-writer-${process.pid}-${Date.now()}`)
mkdirSync(directory, { recursive: true })
try {
  const indexedChunks: Buffer[] = []
  const indexedOffsets: number[] = []
  const indexedIdentities: string[] = []
  const indexedEvents: Array<string | undefined> = []
  const client = new LogWriterWorkerClient({
    directory,
    fileName: 'worker-test.log',
    fileEnabled: true,
    consoleEnabled: false,
    maxFileBytes: 1024,
    retentionDays: 1,
    maxFiles: 3,
    onLine: (chunk, options) => {
      indexedChunks.push(chunk)
      indexedOffsets.push(options?.logOffset ?? -1)
      indexedIdentities.push(options?.logFileIdentity ?? '')
      indexedEvents.push(options?.parsedMetadata?.event)
    }
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
  assert.deepEqual(indexedOffsets, [
    0,
    indexedChunks[0].byteLength,
    indexedChunks[0].byteLength + indexedChunks[1].byteLength,
    indexedChunks[0].byteLength + indexedChunks[1].byteLength + indexedChunks[2].byteLength
  ])
  assert(indexedIdentities.every(Boolean))
  assert.equal(new Set(indexedIdentities).size, 1)
  assert.deepEqual(indexedEvents, ['one', 'two', 'three', 'four'])

  const rotationDirectory = join(directory, 'rotation')
  mkdirSync(rotationDirectory, { recursive: true })
  const rotatedIdentities: string[] = []
  const rotatingClient = new LogWriterWorkerClient({
    directory: rotationDirectory,
    fileName: 'worker-test.log',
    fileEnabled: true,
    consoleEnabled: false,
    maxFileBytes: 20,
    retentionDays: 1,
    maxFiles: 3,
    onLine: (_chunk, options) => rotatedIdentities.push(options?.logFileIdentity ?? '')
  })
  await write(rotatingClient, Buffer.from('{"event":"rotation-one"}\n'))
  await write(rotatingClient, Buffer.from('{"event":"rotation-two"}\n'))
  await rotatingClient.close()
  assert.equal(rotatedIdentities.length, 2)
  assert(rotatedIdentities.every(Boolean))
  assert.notEqual(rotatedIdentities[0], rotatedIdentities[1])
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
