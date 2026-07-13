import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const viewSource = readFileSync('../frontend/src/views/chat/ChatView.vue', 'utf8')
const listSource = readFileSync('../frontend/src/views/chat/ChatMessageList.vue', 'utf8')

assert.match(viewSource, /interface ChatTurnEditingState[\s\S]*conversationId:[\s\S]*turnId:[\s\S]*userMessageId:[\s\S]*assistantMessageId:[\s\S]*content:[\s\S]*displacedDraft:[\s\S]*phase:/, '编辑状态必须完整保存会话、轮次、消息、原文、被替换草稿和阶段')
assert.match(viewSource, /Object\.freeze\(\{[\s\S]{0,500}clientMessageId[\s\S]{0,500}replaceTurnId[\s\S]{0,500}snapshot/, '每次发送必须捕获不可变请求上下文')
assert.match(viewSource, /event\.type === 'message\.started'[\s\S]{0,600}replaceTurnId/, '只有 message.started 才能投影替换旧轮次')
assert.match(viewSource, /ChatStreamHttpError[\s\S]{0,800}chat_replace_conflict/, '替换冲突必须按 typed HTTP code 单独处理')
assert.match(viewSource, /reconcileChatSubmission\(\{[\s\S]{0,400}clientMessageId: request\.clientMessageId[\s\S]{0,400}refreshMessages\(request\.conversationId\)/, '开始前网络错误必须按 clientMessageId 刷新对账')
assert.match(viewSource, /最近一轮已变化，已保留当前草稿/, '替换冲突必须显示中文顶部提示')
assert.doesNotMatch(viewSource, /if \(!controller\.signal\.aborted\) await handleSubmitFailure/, 'message.started 前主动停止也必须进入 clientMessageId 对账，不能遗留 submitting 编辑态')
assert.match(viewSource, /handleSubmitFailure\(error, requestContext, streamStarted, startedTurnId, controller\.signal\.aborted\)/, '主动停止必须把静默标记交给同一失败对账路径')
assert.match(viewSource, /event\.type === 'message\.completed' \|\| event\.type === 'message\.failed'\) streamTerminal = true/, '只允许模型终态事件确认流完成')
assert.match(viewSource, /if \(!streamTerminal\) throw new Error\('模型流已中断，正在确认消息终态'\)/, '流连接结束但没有终态事件时必须进入对账，不能把 streaming 当成功')
assert.match(viewSource, /reconcileChatSubmission/, '接受后断流必须使用有界终态对账，而不是只立即刷新一次')
assert.match(viewSource, /resolveChatReconciliationNotice\(\{ accepted, assistantStatus:[^}]+silent \}\)/, '主动停止与 completed 终态必须通过可测试的提示决策，不能一律显示发送失败')
assert.match(viewSource, /function cancelTurnEdit/, '取消编辑必须是独立的零后端副作用操作')
assert.match(viewSource, /await cancelTurnEdit\(\)[\s\S]{0,300}selectedConversationId\.value = id/, '切换会话前必须先退出编辑态')
assert.match(viewSource, /:editable-message-id="generating \|\| stopping \? undefined : editableUserMessageId"/, '提交或停止门禁期间必须移除编辑入口')
assert.match(viewSource, /@edit-message="beginTurnEdit"/, '消息列表编辑入口必须接入页面状态')

assert.match(listSource, /editableMessageId\?: string/, '消息列表必须只接收一个后端事实可编辑消息 id')
assert.match(listSource, /editingTurnId\?: string/, '消息列表必须能淡化正在编辑的完整轮次')
assert.match(listSource, /edit-message/, '消息列表必须向页面发出编辑事件')
assert.match(listSource, /is-editing-turn/, '编辑中的用户与助手消息必须共享低强调样式')

assert.match(viewSource, /const stopping = ref\(false\)/, '慢 stop HTTP 与旧发送对账期间必须持有独立 stopping gate')
assert.match(viewSource, /if \(generating\.value \|\| stopping\.value/, '发送、编辑或切换必须拒绝跨越 stopping gate')
assert.match(viewSource, /:disabled="generating \|\| stopping"/, 'stopping 期间 composer 必须持续禁用，即使旧 send finally 已把 generating 置 false')
assert.match(viewSource, /const controller = streamController[\s\S]{0,300}stopActiveChatGeneration/, 'stop 必须捕获旧 controller 并交给隔离 helper')

console.log('AI 问答编辑/取消/替换页面竞态接线回归通过')
