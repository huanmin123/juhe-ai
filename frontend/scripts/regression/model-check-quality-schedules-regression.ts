import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  executionStateColor,
  executionStateText,
  triggerKindColor,
  triggerKindText
} from '../../src/views/model-checks/modelCheckFormatters'

const currentDir = dirname(fileURLToPath(import.meta.url))
const frontendRoot = resolve(currentDir, '../..')
const schedulesModalSource = readFileSync(resolve(frontendRoot, 'src/views/model-checks/ModelQualitySchedulesModal.vue'), 'utf8')
const modelChecksViewSource = readFileSync(resolve(frontendRoot, 'src/views/model-checks/ModelChecksView.vue'), 'utf8')
const drawerSource = readFileSync(resolve(frontendRoot, 'src/views/model-checks/ModelCheckRunDetailDrawer.vue'), 'utf8')
const historyListSource = readFileSync(resolve(frontendRoot, 'src/views/model-checks/ModelCheckRunHistoryList.vue'), 'utf8')
const formattersSource = readFileSync(resolve(frontendRoot, 'src/views/model-checks/modelCheckFormatters.ts'), 'utf8')
const typesSource = readFileSync(resolve(frontendRoot, 'src/types/domain/model-checks.ts'), 'utf8')
const apiSource = readFileSync(resolve(frontendRoot, 'src/api/domains/modelChecks.ts'), 'utf8')
const scopedApiSource = readFileSync(resolve(frontendRoot, 'src/composables/useScopedDomainApi.ts'), 'utf8')

// —— executionState 五状态文案与颜色映射 ——
assert.equal(executionStateText('paused'), '已暂停', 'paused 必须显示为已暂停')
assert.equal(executionStateText('running'), '执行中', 'running 必须显示为执行中')
assert.equal(executionStateText('blocked_account'), '账户阻塞', 'blocked_account 必须显示为账户阻塞')
assert.equal(executionStateText('queued'), '排队中', 'queued 必须显示为排队中')
assert.equal(executionStateText('enabled'), '已启用', 'enabled 必须显示为已启用')
assert.equal(executionStateColor('paused'), 'default', 'paused 标签必须为 default 色')
assert.equal(executionStateColor('running'), 'processing', 'running 标签必须为 processing 色')
assert.equal(executionStateColor('blocked_account'), 'warning', 'blocked_account 标签必须为 warning/error 色')
assert.equal(executionStateColor('queued'), 'gold', 'queued 标签必须为 gold 色')
assert.equal(executionStateColor('enabled'), 'green', 'enabled 标签必须为 green 色')

// —— triggerKind 扩展 schedule_now ——
assert.equal(triggerKindText('schedule_now'), '计划立即执行', 'schedule_now 必须显示为计划立即执行')
assert.equal(triggerKindText('manual'), '手动检查', 'manual 文案保持不变')
assert.equal(triggerKindText('scheduled'), '定时检查', 'scheduled 文案保持不变')
assert.equal(triggerKindText('quality_recovery'), '质量恢复', 'quality_recovery 文案保持不变')
assert.ok(triggerKindColor('schedule_now') !== triggerKindColor('manual'), 'schedule_now 标签色必须与 manual 区分')

// —— 检查间隔下限收敛到 1，恢复检查间隔维持 10 ——
assert.match(schedulesModalSource, /v-model:value="form\.intervalMinutes" :min="1" :max="10080"/, '检查间隔输入框 min 必须为 1')
assert.match(schedulesModalSource, /value >= 1 && value <= 10080/, '检查间隔校验必须允许 1 到 10080 的整数')
assert.match(schedulesModalSource, /请输入 1 到 10080 的整数/, '检查间隔校验文案必须为 1 到 10080')
assert.match(schedulesModalSource, /intervalMinutes: 60/, '检查间隔默认值必须保持 60')
assert.match(schedulesModalSource, /v-model:value="form\.recoveryIntervalMinutes" :min="10" :max="10080"/, '恢复检查间隔输入框 min 必须维持 10')
assert.match(schedulesModalSource, /value >= 10 && value <= 10080/, '恢复检查间隔校验必须维持 10 到 10080')
assert.match(schedulesModalSource, /请输入 10 到 10080 的整数/, '恢复检查间隔校验文案必须维持 10 到 10080')
assert.match(schedulesModalSource, /Number\.isInteger\(form\.intervalMinutes\) \|\| form\.intervalMinutes < 1 \|\| form\.intervalMinutes > 10080/, 'save() 必须防御检查间隔超出 1 到 10080')

// —— 计划列表五状态标签与 lastRunScore ——
assert.match(schedulesModalSource, /:color="executionStateColor\(item\.executionState\)"/, '计划状态标签必须使用 executionState 颜色映射')
assert.match(schedulesModalSource, /\{\{ executionStateText\(item\.executionState\) \}\}/, '计划状态标签必须使用 executionState 文案映射')
assert.doesNotMatch(schedulesModalSource, /item\.enabled \? '运行中' : '已暂停'/, '不得再用 enabled 双值状态标签')
assert.match(schedulesModalSource, /currentEnforcementAction === 'quality_isolate'" color="red">质量隔离中/, '质量隔离中红标必须保留')
assert.match(schedulesModalSource, /item\.lastRunScore === null \|\| item\.lastRunScore === undefined/, '上次结果必须识别 lastRunScore 为 null 或缺失')
assert.match(schedulesModalSource, /scoreMissing \? '-' : `\$\{item\.lastRunScore\} 分`/, '上次结果行必须显示 lastRunScore，缺失时显示 -')

// —— 立即执行：行操作 + 多选批量 ——
assert.match(schedulesModalSource, /key: 'run-now', label: running \? '执行中' : '立即执行'/, '行操作必须包含立即执行，执行中时提示执行中')
assert.match(schedulesModalSource, /const running = item\.executionState === 'running'/, '立即执行禁用必须依据 executionState=running')
assert.match(schedulesModalSource, /disabled: running/, 'executionState=running 时立即执行必须禁用')
assert.match(schedulesModalSource, /<a-checkbox/, '计划列表每行必须提供 a-checkbox 多选')
assert.match(schedulesModalSource, /已选 \{\{ selectedScheduleCount \}\} 项/, '工具条必须显示已选 N 项')
assert.match(schedulesModalSource, /批量立即执行/, '工具条必须提供批量立即执行按钮')
assert.match(schedulesModalSource, /:disabled="!selectedScheduleCount"/, '无选中时批量立即执行必须禁用')
assert.match(schedulesModalSource, /watch\(\(\) => props\.page, \(\) => \{\s*\n\s*selectedScheduleIds\.value = new Set\(\)\s*\n\s*\}\)/, '翻页必须清空已选计划，保证已选计数与实际提交一致')
assert.match(schedulesModalSource, /emit\('run-now-batch', items\.map\(\(item\) => \(\{ scheduleId: item\.id, revision: item\.revision \}\)\)\)/, '批量 emit 必须携带 scheduleId 与 revision')
assert.doesNotMatch(schedulesModalSource, /<a-table|ResponsiveDataList/, '计划列表必须保持 CSS grid 自绘列表，不得引入 a-table')

// —— 父页面接线：单条/批量调用、409、汇总反馈、防迟到 ——
assert.match(modelChecksViewSource, /@run-now="runScheduleNow"/, '父页面必须接入单条立即执行事件')
assert.match(modelChecksViewSource, /@run-now-batch="runSchedulesNowBatch"/, '父页面必须接入批量立即执行事件')
assert.match(modelChecksViewSource, /runNowQualitySchedule\(item\.id, \{ revision: item\.revision \}, modelCheckScopeParams\.value\)/, '单条立即执行必须携带 revision 与 scope')
assert.match(modelChecksViewSource, /runNowQualitySchedulesBatch\(items, modelCheckScopeParams\.value\)/, '批量立即执行必须一次性调用批量端点')
assert.match(modelChecksViewSource, /message\.success\('已发起立即执行'\)/, '单条成功必须提示已发起立即执行')
assert.match(modelChecksViewSource, /message\.warning\('该账户正在执行检测'\)/, '409 冲突必须提示该账户正在执行检测')
assert.match(modelChecksViewSource, /error\.response\?\.status === 409/, '必须沿用 409 冲突识别模式')
assert.match(modelChecksViewSource, /受理 \$\{accepted\} 条，跳过 \$\{skipped\} 条/, '批量反馈必须汇总受理与跳过计数')
assert.match(modelChecksViewSource, /'already_running'/, '批量跳过原因必须覆盖 already_running')
assert.match(modelChecksViewSource, /'stale_revision'/, '批量跳过原因必须覆盖 stale_revision')
assert.match(modelChecksViewSource, /'not_found'/, '批量跳过原因必须覆盖 not_found')
assert.match(modelChecksViewSource, /'invalid'/, '批量跳过原因必须覆盖 invalid')
assert.match(modelChecksViewSource, /isCurrentScheduleRunNowRequest\(requestId, contextKey\)/, '立即执行反馈必须沿用 requestId 防迟到模式')
assert.match(modelChecksViewSource, /await loadSchedules\(\)/, '立即执行完成后必须刷新计划列表')

// —— 两面 API 路径 ——
assert.match(apiSource, /`\/model-checks\/quality-schedules\/\$\{scheduleId\}\/run-now`/, '管理面单条 run-now 路径必须正确')
assert.match(apiSource, /'\/model-checks\/quality-schedules\/run-now-batch'/, '管理面批量 run-now 路径必须正确')
assert.match(apiSource, /`\/my-model-checks\/quality-schedules\/\$\{scheduleId\}\/run-now`/, '自助面单条 run-now 路径必须正确')
assert.match(apiSource, /'\/my-model-checks\/quality-schedules\/run-now-batch'/, '自助面批量 run-now 路径必须正确')
assert.match(apiSource, /http\.post\('\/model-checks\/quality-schedules\/run-now-batch', \{ items \}/, '批量请求体必须为 { items }')
assert.equal((apiSource.match(/quality-schedules\/run-now-batch/g) ?? []).length, 2, 'run-now-batch 路径必须且只在两面 API 各出现一次')

// —— scoped API 分叉 ——
assert.match(scopedApiSource, /runNowQualitySchedule: \(scheduleId/, 'useScopedModelChecksApi 必须分叉 runNowQualitySchedule')
assert.match(scopedApiSource, /runNowQualitySchedulesBatch: \(items/, 'useScopedModelChecksApi 必须分叉 runNowQualitySchedulesBatch')
assert.match(scopedApiSource, /api\.modelChecks\.runNowQualitySchedule\(scheduleId, payload, params\)/, '管理面 run-now 必须透传 scope 参数')
assert.match(scopedApiSource, /api\.myModelChecks\.runNowQualitySchedule\(scheduleId, payload\)/, '自助面 run-now 不携带 scope 参数')
assert.match(scopedApiSource, /api\.myModelChecks\.runNowQualitySchedulesBatch\(items\)/, '自助面批量 run-now 不携带 scope 参数')

// —— 详情抽屉 qualityDecision 未记录兜底 ——
assert.match(drawerSource, /decisionTriggeredText\(run\.qualityDecision\.triggered\)/, '判定行必须经未记录兜底')
assert.match(drawerSource, /decisionScoreText\(run\.qualityDecision\.score, run\.qualityDecision\.threshold\)/, '分数阈值行必须经未记录兜底')
assert.match(drawerSource, /decisionActionText\(run\.qualityDecision\.configuredAction\)/, '处罚方式行必须经未记录兜底')
assert.match(drawerSource, /decisionResultText\(run\.qualityDecision\.result\)/, '执行结果行必须经未记录兜底')
assert.match(drawerSource, /value == null\s*\n\s*\? '未记录'/, 'qualityDecision 字段缺失（含 JSON null）必须显示未记录而非假值文案')
assert.match(drawerSource, /run\.qualityDecision\.message == null \? '未记录' : run\.qualityDecision\.message/, '处罚详情缺失（含 JSON null）必须显示未记录')
assert.match(drawerSource, /healthSyncText[\s\S]*?value === undefined[\s\S]{0,40}\? '未记录'/, 'healthSyncResult 缺失必须显示未记录而非无需同步')
assert.match(drawerSource, /triggerKindText\(run\.triggerKind\)/, '详情抽屉检查来源必须使用共享 triggerKind 文案')

// —— 历史列表 triggerKind 标签 ——
assert.match(historyListSource, /triggerKindColor\(record\.triggerKind\)"/, '历史列表 triggerKind 标签必须使用共享颜色')
assert.match(historyListSource, /\{\{ triggerKindText\(record\.triggerKind\) \}\}/, '历史列表 triggerKind 标签必须使用共享文案')
assert.doesNotMatch(historyListSource, /function triggerText|function triggerColor/, '历史列表不得保留本地 triggerKind 文案实现')

// —— 类型契约 ——
assert.match(typesSource, /export type ModelCheckTriggerKind = 'manual' \| 'scheduled' \| 'quality_recovery' \| 'schedule_now'/, 'triggerKind 联合类型必须包含 schedule_now')
assert.match(typesSource, /export type ModelQualityScheduleExecutionState = 'paused' \| 'running' \| 'blocked_account' \| 'queued' \| 'enabled'/, 'executionState 必须是五值枚举')
assert.match(typesSource, /executionState: ModelQualityScheduleExecutionState/, '计划类型必须包含 executionState')
assert.match(typesSource, /accountStatus\?: string/, '计划类型必须包含可选 accountStatus')
assert.match(typesSource, /lastRunScore\?: number \| null/, '计划类型必须包含可空 lastRunScore')
assert.match(typesSource, /interface ModelQualityScheduleRunNowResult[\s\S]*?status: 'started'/, '单条 run-now 响应类型 status 必须为 started')
assert.match(typesSource, /'started' \| 'already_running' \| 'not_found' \| 'stale_revision' \| 'invalid'/, '批量结果状态必须覆盖五种取值')

// —— formatters 源内五状态映射存在（防映射被删后运行时断言失效）——
assert.match(formattersSource, /export function executionStateText/, 'formatters 必须导出 executionStateText')
assert.match(formattersSource, /export function executionStateColor/, 'formatters 必须导出 executionStateColor')
assert.match(formattersSource, /export function triggerKindText/, 'formatters 必须导出 triggerKindText')

// —— 题库测试未执行兜底：新 executed 字段 + 历史空执行项数据 ——
assert.match(typesSource, /executed\?: boolean/, '题库小计类型必须包含可选 executed 字段')
assert.match(formattersSource, /value\.executed === false/, '题库小计必须优先消费后端 executed=false 事实')
assert.match(formattersSource, /items\.some\(\(item\) => item\.verdict === 'passed' \|\| item\.verdict === 'failed'\)/, '历史数据无 executed 字段时必须以 passed/failed 判定兜底（unavailable 全请求失败形态同为未执行）')
assert.match(drawerSource, /quizSummary\.executed === false \? '未执行题库测试'/, '题库未执行时小计必须显示未执行而非 31/31 满分误导')

console.log('模型质量计划回归通过：间隔下限 1、五状态标签、lastRunScore、run-now 单条/批量、未记录兜底与 schedule_now 文案')
