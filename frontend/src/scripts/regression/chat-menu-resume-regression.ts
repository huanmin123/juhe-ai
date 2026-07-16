import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { reconcileChatSubmission } from '../../views/chat/chatTurnReconciliation'

const acceptedStops: string[] = []
const accepted = await reconcileChatSubmission({
  initialAcceptedTurnId: 'turn_running',
  initialAssistantStatus: 'streaming',
  getSubmissionStatus: async () => { throw { response: { status: 503 } } },
  listMessages: async () => [],
  stop: async (turnId) => { acceptedStops.push(turnId) },
  wait: async () => undefined,
  maxAttempts: 2
})
assert.equal(accepted.accepted, true)
assert.deepEqual(acceptedStops, [], 'accepted + streaming 遇到临时错误必须交给应用级 runtime 重连，绝不自动 stop')

const viewSource = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const routerSource = readFileSync(new URL('../../router/index.ts', import.meta.url), 'utf8')
const authSource = readFileSync(new URL('../../composables/useAuth.ts', import.meta.url), 'utf8')
const mainSource = readFileSync(new URL('../../main.ts', import.meta.url), 'utf8')
const layoutSource = readFileSync(new URL('../../layouts/AppLayout.vue', import.meta.url), 'utf8')

assert.match(routerSource, /path:\s*'\/my-chat'[\s\S]{0,300}keepAlive:\s*true/, 'AI 问答路由必须启用 KeepAlive')
assert.match(viewSource, /onActivated/, '返回 AI 问答时必须恢复页面监听与轻量同步')
assert.match(viewSource, /onDeactivated/, '离开 AI 问答时必须只暂停页面监听和定时器')
assert.doesNotMatch(viewSource, /onBeforeUnmount\([\s\S]{0,900}streamController\?\.abort\(\)/, '页面卸载不得中止已接受生成')
assert.match(viewSource, /chatGenerationRuntime\.subscribe/, '页面必须订阅应用级生成运行态')
assert.match(viewSource, /chatGenerationRuntime\.start/, '发送必须交给应用级生成运行态')
assert.match(viewSource, /state === 'forbidden'[\s\S]{0,300}chatGenerationRuntime\.blockConversation/, '403 必须解除并阻断当前会话 runtime 投影')
assert.match(viewSource, /const conversationForbidden[\s\S]{0,1200}const submissionBlocked[^\n]+conversationForbidden/, '403 后 UI 必须显式进入不可发送状态')
assert.match(viewSource, /conversationAccessEpoch\.value[\s\S]{0,300}isConversationBlocked/, 'runtime 非响应式门禁必须通过页面 epoch 驱动 composer 立即禁用')
assert.match(viewSource, /isConversationBlocked[\s\S]{0,500}readConversation/, '重新选择被禁止会话时必须在读取 IndexedDB 正文前检查持久门禁')
assert.match(viewSource, /chatGenerationRuntime\.start\([\s\S]{0,700}catch[\s\S]{0,500}(?:clearPendingConfirmation|handleSubmitFailure)/, 'runtime.start 同步失败必须恢复 pending/generating 并给出可读错误')
assert.match(viewSource, /turn\.error\?\.status === 403[\s\S]{0,500}messages\.value = \[\][\s\S]{0,700}finally[\s\S]{0,500}composer\.value\?\.restore/, '初始 POST\/SSE 403 必须立即隐藏正文，并在失败收口 finally 恢复草稿')
assert.match(viewSource, /finally[\s\S]{0,500}generating\.value = false[\s\S]{0,500}clearPendingConfirmation/, '初始稳定失败必须在 finally 恢复 generating 与 pending 门禁')
assert.match(viewSource, /ChatRuntimeReconciliationScheduler/, 'reconnect_exhausted 权威同步必须使用有界退避调度器')
assert.match(viewSource, /requestRuntimeReconciliationSync/, '页面事件与 5 秒轮询必须统一进入可重试的权威同步门禁')
assert.doesNotMatch(viewSource, /setInterval\([\s\S]{0,500}reconciliationReason\) void refreshConversationFromSync/, '5 秒轮询不得绕过退避调度器高频同步')
assert.match(viewSource, /state === 'superseded'[\s\S]{0,300}chatGenerationRuntime\.allowConversation/, '认证恢复并同步成功后必须允许 runtime 重附着')
assert.match(viewSource, /turn\.turnId && turn\.clientMessageId[\s\S]{0,700}activeStopTarget = active/, '返回旧会话时必须从 runtime 重建精确停止目标')
assert.match(mainSource, /chatGenerationRuntime[\s\S]{0,500}(?:activateAccount|switchAccount)/, '应用入口必须根据登录账户切换 runtime 身份')
assert.match(mainSource, /activateChatConversationSyncAccount/, '应用入口必须同步切换会话 sync 身份栅栏')
assert.match(authSource, /clearAccount/, '明确退出登录必须清理当前账户聊天缓存')
assert.match(authSource, /invalidateChatConversationSyncAccount[\s\S]{0,400}drainChatConversationSyncAccount[\s\S]{0,400}clearAccount/, '退出必须先失效并排空旧账户同步，再清理缓存')
assert.match(layoutSource, /clearCurrentAccountChatState/, '撤销当前会话必须执行与明确退出相同的聊天清理')
assert.match(layoutSource, /route-view-host/, '路由内容必须有独立常驻容器，切页反馈不能销毁 KeepAlive')
assert.doesNotMatch(layoutSource, /<router-view\s+v-else/, 'router-view 不能作为 routeSwitching 的 v-else 分支被反复卸载')

console.log('AI 问答菜单切换续跑、KeepAlive 与身份清理回归通过')
