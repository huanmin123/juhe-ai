import assert from 'node:assert/strict'

import {
  AssistantTimeline,
  type AssistantJsonObject,
  type AssistantToolCallBlock
} from '../../modules/chat/chat-assistant-timeline.js'

function orderedBlocksAndStableToolPosition(): void {
  const timeline = new AssistantTimeline()
  timeline.appendText('文本 A')
  timeline.appendReasoning('思考')
  timeline.startTool({ callId: 'search-1', toolType: 'web_search' })
  timeline.appendText('文本 B')
  const beforeUpdate = timeline.snapshot()
  const searchOneBefore = beforeUpdate.contentBlocks.find(
    (block): block is AssistantToolCallBlock => block.type === 'tool_call' && block.callId === 'search-1'
  )
  assert(searchOneBefore, 'started tool block should exist')

  timeline.updateTool({ callId: 'search-1', status: 'completed', item: { query: '时间线' } })
  timeline.startTool({ callId: 'search-2', toolType: 'web_search' })
  const afterUpdate = timeline.snapshot()

  assert.deepEqual(afterUpdate.contentBlocks.map((block) => block.type), [
    'output_text', 'reasoning', 'tool_call', 'output_text', 'tool_call'
  ])
  assert.deepEqual(afterUpdate.contentBlocks.map((block) => block.order), [1, 2, 3, 4, 5])
  const searchOneAfter = afterUpdate.contentBlocks.find(
    (block): block is AssistantToolCallBlock => block.type === 'tool_call' && block.callId === 'search-1'
  )
  assert(searchOneAfter, 'updated tool block should exist')
  assert.equal(searchOneAfter.blockId, searchOneBefore.blockId, 'tool updates must keep blockId')
  assert.equal(searchOneAfter.order, searchOneBefore.order, 'tool updates must keep order')
  assert.equal(searchOneAfter.status, 'completed')
  assert.deepEqual(searchOneAfter.item, { query: '时间线' })
  assert.equal(afterUpdate.contentText, '文本 A文本 B', 'contentText should concatenate output_text blocks by order')
}

function consecutiveTextAndReasoningMergeOnlyWithinRuns(): void {
  const timeline = new AssistantTimeline()
  timeline.appendText('A')
  timeline.appendText('B')
  timeline.appendReasoning('R1')
  timeline.appendReasoning('R2')
  timeline.appendText('C')
  timeline.appendReasoning('R3')
  const blocks = timeline.snapshot().contentBlocks

  assert.deepEqual(blocks.map((block) => block.type), ['output_text', 'reasoning', 'output_text', 'reasoning'])
  assert.equal(blocks[0]?.type === 'output_text' ? blocks[0].text : undefined, 'AB')
  assert.equal(blocks[1]?.type === 'reasoning' ? blocks[1].text : undefined, 'R1R2')
  assert.equal(blocks[2]?.type === 'output_text' ? blocks[2].text : undefined, 'C')
  assert.equal(blocks[3]?.type === 'reasoning' ? blocks[3].text : undefined, 'R3')
}

function completedReasoningBlockCannotBeReopenedByAnotherDelta(): void {
  const timeline = new AssistantTimeline()
  timeline.appendReasoning('第一段')
  const first = timeline.snapshot().contentBlocks[0]
  assert(first?.type === 'reasoning')
  timeline.completeBlock(first.blockId)
  timeline.appendReasoning('第二段')
  const blocks = timeline.snapshot().contentBlocks

  assert.deepEqual(blocks.map((block) => block.type), ['reasoning', 'reasoning'])
  assert.equal(blocks[0]?.type === 'reasoning' ? blocks[0].status : undefined, 'completed')
  assert.equal(blocks[0]?.type === 'reasoning' ? blocks[0].text : undefined, '第一段')
  assert.equal(blocks[1]?.type === 'reasoning' ? blocks[1].status : undefined, 'started')
  assert.equal(blocks[1]?.type === 'reasoning' ? blocks[1].text : undefined, '第二段')
}

function emptyDeltasDoNotCreateOrSplitBlocks(): void {
  const emptyTimeline = new AssistantTimeline()
  assert.equal(emptyTimeline.appendText(''), undefined)
  assert.equal(emptyTimeline.appendReasoning(''), undefined)
  assert.deepEqual(emptyTimeline.snapshot().contentBlocks, [], 'fresh empty deltas must not allocate blocks')

  const existingText = new AssistantTimeline()
  existingText.appendText('正文')
  const textBefore = existingText.snapshot()
  assert.equal(existingText.appendText(''), undefined)
  assert.equal(existingText.appendReasoning(''), undefined)
  assert.deepEqual(existingText.snapshot(), textBefore, 'empty deltas must not mutate or split an existing text block')

  const existingReasoning = new AssistantTimeline()
  existingReasoning.appendReasoning('思考')
  const reasoningBefore = existingReasoning.snapshot()
  assert.equal(existingReasoning.appendReasoning(''), undefined)
  assert.equal(existingReasoning.appendText(''), undefined)
  assert.deepEqual(existingReasoning.snapshot(), reasoningBefore, 'empty deltas must not mutate or split an existing reasoning block')
}

function finalizeTerminalizesActiveBlocksWithoutRewritingCompletedBlocks(): void {
  const timeline = new AssistantTimeline()
  timeline.appendReasoning('已完成思考')
  const completedReasoning = timeline.snapshot().contentBlocks[0]
  assert.equal(completedReasoning?.type, 'reasoning')
  timeline.completeBlock(completedReasoning!.blockId)
  timeline.startTool({ callId: 'search-completed', toolType: 'web_search' })
  timeline.updateTool({ callId: 'search-completed', status: 'completed' })
  timeline.appendReasoning('未完成思考')
  timeline.startTool({ callId: 'search-active', toolType: 'web_search' })

  timeline.finalize('failed')
  const failed = timeline.snapshot()
  assert.equal(failed.status, 'failed')
  assert.equal(failed.contentBlocks[0]?.type === 'reasoning' ? failed.contentBlocks[0].status : undefined, 'completed')
  assert.equal(failed.contentBlocks[1]?.type === 'tool_call' ? failed.contentBlocks[1].status : undefined, 'completed')
  assert.equal(failed.contentBlocks[2]?.type === 'reasoning' ? failed.contentBlocks[2].status : undefined, 'failed')
  assert.equal(failed.contentBlocks[3]?.type === 'tool_call' ? failed.contentBlocks[3].status : undefined, 'failed')

  const failedSnapshot = timeline.snapshot()
  timeline.finalize('canceled')
  assert.deepEqual(timeline.snapshot(), failedSnapshot, 'a finalized timeline must keep its first terminal state')
}

function cancellationTerminalizesUpdatedActiveBlocks(): void {
  const timeline = new AssistantTimeline()
  timeline.appendReasoning('取消前思考')
  const reasoning = timeline.snapshot().contentBlocks[0]
  assert(reasoning?.type === 'reasoning')
  timeline.startTool({ callId: 'search-canceled', toolType: 'web_search' })
  timeline.updateTool({ callId: 'search-canceled', status: 'updated', item: { phase: 'running' } })

  timeline.finalize('canceled')
  const snapshot = timeline.snapshot()
  assert.equal(snapshot.status, 'canceled')
  assert.equal(snapshot.contentBlocks[0]?.type === 'reasoning' ? snapshot.contentBlocks[0].status : undefined, 'canceled')
  assert.equal(snapshot.contentBlocks[1]?.type === 'tool_call' ? snapshot.contentBlocks[1].status : undefined, 'canceled')
}

function terminalToolStartIsIdempotentAndToolTypeCannotDrift(): void {
  const timeline = new AssistantTimeline()
  timeline.startTool({ callId: 'search-terminal', toolType: 'web_search', item: { query: '初始' } })
  timeline.updateTool({ callId: 'search-terminal', status: 'completed' })
  const beforeDuplicateStart = timeline.snapshot()
  timeline.startTool({ callId: 'search-terminal', toolType: 'web_search', item: { query: '迟到' } })
  assert.deepEqual(timeline.snapshot(), beforeDuplicateStart, 'completed tool must ignore duplicate start payload')
  assert.throws(
    () => timeline.startTool({ callId: 'search-terminal', toolType: 'different_tool' }),
    /工具类型不一致/
  )
}

function nonSerializableToolItemIsRejected(): void {
  const circular: Record<string, unknown> = {}
  circular.self = circular
  const invalidItems: Array<[string, unknown]> = [
    ['undefined', { invalid: undefined }],
    ['function', { invalid: () => 'ignored' }],
    ['symbol', { invalid: Symbol('ignored') }],
    ['NaN', { invalid: Number.NaN }],
    ['Infinity', { invalid: Number.POSITIVE_INFINITY }],
    ['negative Infinity', { invalid: Number.NEGATIVE_INFINITY }],
    ['BigInt', { invalid: BigInt(1) }],
    ['circular reference', circular]
  ]
  for (const [label, invalidItem] of invalidItems) {
    const timeline = new AssistantTimeline()
    assert.throws(
      () => timeline.startTool({
        callId: `invalid-${label}`,
        toolType: 'web_search',
        item: invalidItem as AssistantJsonObject
      }),
      /不可序列化为严格 JSON/,
      `${label} must be rejected instead of being dropped or rewritten`
    )
    assert.deepEqual(timeline.snapshot().contentBlocks, [], `${label} rejection must not create a tool block`)
  }

  const timeline = new AssistantTimeline()
  const validItem = { query: '保留', values: [null, true, 1, 'text', { nested: 'value' }] }
  timeline.startTool({ callId: 'valid-json', toolType: 'web_search', item: validItem })
  const validTool = timeline.snapshot().contentBlocks[0]
  assert(validTool?.type === 'tool_call')
  assert.deepEqual(validTool.item, validItem, 'valid recursive JSON values must be preserved exactly')
  const beforeInvalidUpdate = timeline.snapshot()
  assert.throws(
    () => timeline.updateTool({
      callId: 'valid-json',
      status: 'completed',
      item: { invalid: Number.NaN } as unknown as AssistantJsonObject
    }),
    /不可序列化为严格 JSON/
  )
  assert.deepEqual(timeline.snapshot(), beforeInvalidUpdate, 'invalid update item must not change status or the previous item')
}

function finalizedTimelineRejectsLateBlockMutations(): void {
  const timeline = new AssistantTimeline()
  const reasoning = timeline.appendReasoning('思考')
  const tool = timeline.startTool({ callId: 'late-tool', toolType: 'web_search' })
  timeline.finalize('completed')
  const terminalSnapshot = timeline.snapshot()

  assert.throws(() => timeline.appendText('迟到正文'), /时间线已进入终态/)
  assert.throws(() => timeline.appendReasoning('迟到思考'), /时间线已进入终态/)
  assert.throws(() => timeline.startTool({ callId: 'late-start', toolType: 'web_search' }), /时间线已进入终态/)
  assert.throws(() => timeline.updateTool({ callId: tool.callId, status: 'updated' }), /时间线已进入终态/)
  assert.throws(() => timeline.completeBlock(reasoning!.blockId), /时间线已进入终态/)
  assert.deepEqual(timeline.finalize('failed'), terminalSnapshot, 'repeated finalize must preserve the first terminal snapshot')
  assert.deepEqual(timeline.snapshot(), terminalSnapshot)
}

function snapshotsAreCopiesAndBlocksRemainStable(): void {
  const timeline = new AssistantTimeline()
  timeline.appendText('原文')
  const snapshot = timeline.snapshot()
  const block = snapshot.contentBlocks[0]
  assert(block)
  snapshot.contentBlocks[0] = { ...block, blockId: '改写', order: 99 } as typeof block
  if (snapshot.contentBlocks[0]?.type === 'output_text') snapshot.contentBlocks[0].text = '外部改写'
  const current = timeline.snapshot().contentBlocks[0]
  assert.equal(current?.blockId, block.blockId)
  assert.equal(current?.order, 1)
  assert.equal(current?.type === 'output_text' ? current.text : undefined, '原文')
}

orderedBlocksAndStableToolPosition()
consecutiveTextAndReasoningMergeOnlyWithinRuns()
completedReasoningBlockCannotBeReopenedByAnotherDelta()
emptyDeltasDoNotCreateOrSplitBlocks()
finalizeTerminalizesActiveBlocksWithoutRewritingCompletedBlocks()
cancellationTerminalizesUpdatedActiveBlocks()
terminalToolStartIsIdempotentAndToolTypeCannotDrift()
nonSerializableToolItemIsRejected()
finalizedTimelineRejectsLateBlockMutations()
snapshotsAreCopiesAndBlocksRemainStable()

console.log('AI 问答助手有序时间线回归通过')
