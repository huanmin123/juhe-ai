import assert from 'node:assert/strict'

import { drainShardAggregation } from '../maintenance/mockdata/maintenance/derived-cache.js'

function sequence(values: number[]): () => number {
  let index = 0
  return () => values[index++] ?? 0
}

assert.equal(
  drainShardAggregation(sequence([0, 0, 40, 0, 0, 0, 0]), 64),
  40,
  'aggregation must continue across empty shard pages before reaching populated shards'
)

assert.equal(
  drainShardAggregation(sequence([10, 0, 20, 0, 0]), 32),
  30,
  'a populated batch must reset the full empty-shard scan budget'
)

assert.equal(
  drainShardAggregation(sequence([0]), 0),
  0,
  'an empty catalog must still terminate after one empty batch'
)

console.log('mockdata shard aggregation drain regression passed')
