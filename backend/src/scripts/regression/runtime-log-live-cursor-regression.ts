import { strict as assert } from 'node:assert'

import { RuntimeLogIndexStream, setRuntimeLogLineSink } from '../../modules/runtime-logs/runtime-log-stream.js'

const captured: Array<{
  line: string
  options?: { sourceKey?: string; logFile?: string; logFileIdentity?: string; logOffset?: number }
}> = []
setRuntimeLogLineSink((line, options) => captured.push({ line, options }))
const stream = new RuntimeLogIndexStream()
stream.writeIndexedChunk(Buffer.from('{"event":"one"}\n{"event":"two"}\n'), {
  logFile: 'C:/logs/juhe-ai.log',
  logFileIdentity: 'generation-a',
  logOffset: 10
}, () => undefined)
stream.writeIndexedChunk(Buffer.from('{"event":"three"}\n'), {
  logFile: 'C:/logs/juhe-ai.log',
  logFileIdentity: 'generation-b',
  logOffset: 42
}, () => undefined)

assert.equal(captured.length, 3)
assert.equal(captured[0].options?.sourceKey, 'generation-a:10')
assert.equal(captured[0].options?.logOffset, 10)
assert.equal(captured[1].options?.sourceKey, `generation-a:${10 + Buffer.byteLength('{"event":"one"}\n')}`)
assert.equal(captured[2].options?.sourceKey, 'generation-b:42')
setRuntimeLogLineSink(undefined)
console.log('运行日志实时游标回归通过')
