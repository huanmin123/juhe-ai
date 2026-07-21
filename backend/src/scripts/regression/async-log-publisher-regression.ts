import { strict as assert } from 'node:assert'

import { AsyncLogPublisher } from '../../shared/logging/async-log-publisher.js'

const delivered: string[] = []
const publisher = new AsyncLogPublisher({
  maxNormalEvents: 2,
  maxFailureEvents: 1,
  maxBytes: 8,
  maxFailureBytes: 16,
  destinations: [{
    write(chunk, callback) {
      setTimeout(() => {
        delivered.push(chunk.toString())
        callback()
      }, 5)
    }
  }]
})

assert.equal(publisher.enqueue(Buffer.from('one\n'), 'normal'), true)
assert.equal(publisher.enqueue(Buffer.from('two\n'), 'normal'), true)
assert.equal(publisher.enqueue(Buffer.from('three\n'), 'normal'), false)
assert.equal(publisher.enqueue(Buffer.from('failure\n'), 'failure'), true)
assert.equal(publisher.enqueue(Buffer.from('failure-2\n'), 'failure'), false)
const stats = publisher.stats()
assert.equal(stats.normalDropped, 1)
assert.equal(stats.failureDropped, 1)
assert(stats.pendingEvents <= 3)

await publisher.flush()
assert(delivered.includes('failure\n'))
await publisher.close()

let blockedDestinationEnteredResolve!: () => void
const blockedDestinationEntered = new Promise<void>((resolve) => { blockedDestinationEnteredResolve = resolve })
const blockedPublisher = new AsyncLogPublisher({
  maxNormalEvents: 1,
  maxFailureEvents: 1,
  maxBytes: 1024,
  maxFailureBytes: 1024,
  destinations: [{
    write() {
      // Simulate a writer that never acknowledges; shutdown must remain bounded.
      blockedDestinationEnteredResolve()
    }
  }]
})
blockedPublisher.enqueue(Buffer.from('blocked\n'))
await blockedDestinationEntered
assert.equal(blockedPublisher.stats().pendingEvents, 1)
assert.equal(blockedPublisher.stats().pendingBytes, Buffer.byteLength('blocked\n'))
const shutdownStartedAt = Date.now()
assert.equal(await blockedPublisher.closeWithin(20), false)
assert(Date.now() - shutdownStartedAt < 200)
console.log('异步日志发布器回归通过')
