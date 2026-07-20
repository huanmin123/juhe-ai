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

await new Promise((resolve) => setTimeout(resolve, 40))
assert(delivered.includes('failure\n'))
publisher.close()
console.log('异步日志发布器回归通过')
