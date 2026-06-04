<template>
  <section class="stream-policy-shell">
    <a-collapse v-model:activeKey="activeKeys" class="stream-policy-collapse" expand-icon-position="end">
      <a-collapse-panel key="stream">
        <template #header>
          <div class="policy-summary">
            <div class="policy-title-row">
              <h4>账户流式拦截规则</h4>
              <a-space class="policy-title-tags" :size="6" wrap>
                <a-tag color="purple">{{ enabledRuleCount }}/{{ rules.length }} 启用</a-tag>
                <a-tag color="blue">继承默认和管理端策略</a-tag>
                <a-tag v-if="readonly" color="default">只读</a-tag>
              </a-space>
            </div>
          </div>
        </template>

        <div class="policy-content">
          <a-space class="policy-actions" :size="6" wrap>
            <a-button size="small" @click="guideOpen = true">
              <template #icon><question-circle-outlined /></template>
              配置指南
            </a-button>
            <a-button v-if="!readonly" size="small" @click="addRule">添加规则</a-button>
            <a-button size="small" :disabled="rules.length === 0" @click="expandAll">展开全部</a-button>
            <a-button size="small" :disabled="rules.length === 0" @click="collapseAll">收起全部</a-button>
            <a-popconfirm v-if="!readonly" title="确定清空当前账户追加规则吗？" ok-text="清空" cancel-text="取消" @confirm="clearRules">
              <a-button size="small" :disabled="rules.length === 0">清空规则</a-button>
            </a-popconfirm>
          </a-space>

          <a-empty v-if="rules.length === 0" class="compact-empty" description="当前账户没有追加流式拦截规则">
            <a-button v-if="!readonly" type="primary" @click="addRule">添加账户规则</a-button>
          </a-empty>

          <a-collapse v-else v-model:activeKey="ruleKeys" class="rule-collapse" ghost>
            <a-collapse-panel v-for="(rule, index) in rules" :key="ruleKey(index)" class="rule-panel" :class="{ disabled: rule.enabled === false }">
              <template #header>
                <div class="rule-summary">
                  <div class="rule-summary-main">
                    <a-switch v-model:checked="rule.enabled" size="small" checked-children="启" un-checked-children="停" :disabled="readonly" @click.stop />
                    <a-tag color="blue">{{ rule.priority ?? '-' }}</a-tag>
                    <a-tag color="cyan">{{ actionLabel(rule) }}</a-tag>
                    <a-tag v-if="positiveInt(rule.avoidanceTtlSeconds)">{{ positiveInt(rule.avoidanceTtlSeconds) }}s</a-tag>
                    <strong>{{ rule.name || '未命名规则' }}</strong>
                  </div>
                  <span class="rule-condition-summary">{{ conditionSummary(rule) }}</span>
                </div>
              </template>

              <template v-if="!readonly" #extra>
                <a-space class="rule-actions" wrap @click.stop>
                  <a-button size="small" :disabled="index === 0" @click="moveRule(index, -1)">上移</a-button>
                  <a-button size="small" :disabled="index === rules.length - 1" @click="moveRule(index, 1)">下移</a-button>
                  <a-button size="small" danger @click="removeRule(index)">删除</a-button>
                </a-space>
              </template>

              <div class="rule-editor">
                <div class="form-grid compact">
                  <a-form-item label="规则名称">
                    <a-input v-model:value="rule.name" :disabled="readonly" placeholder="例如 屏蔽某中转广告" />
                  </a-form-item>
                  <a-form-item label="优先级">
                    <a-input-number v-model:value="rule.priority" :disabled="readonly" :min="1" :max="9999" style="width: 100%" />
                  </a-form-item>
                </div>

                <div class="form-grid matcher-grid">
                  <a-form-item label="SSE event 类型">
                    <a-input v-model:value="rule.eventTypes" :disabled="readonly" placeholder="response.failed, message" />
                  </a-form-item>
                  <a-form-item label="data.type">
                    <a-input v-model:value="rule.dataTypes" :disabled="readonly" placeholder="response.output_text.delta" />
                  </a-form-item>
                  <a-form-item label="error.code">
                    <a-input v-model:value="rule.errorCodes" :disabled="readonly" placeholder="cyber_policy" />
                  </a-form-item>
                  <a-form-item label="error.type">
                    <a-input v-model:value="rule.errorTypes" :disabled="readonly" placeholder="server_error" />
                  </a-form-item>
                  <a-form-item label="SSE data文本包含">
                    <a-textarea v-model:value="rule.textIncludes" :disabled="readonly" :rows="1" auto-size placeholder="匹配当前单个 SSE 事件 data 文本，多个关键词用逗号、分号或换行分隔" />
                  </a-form-item>
                  <a-form-item label="SSE data文本不包含">
                    <a-textarea v-model:value="rule.textExcludes" :disabled="readonly" :rows="1" auto-size placeholder="当前事件 data 文本包含这些关键词时不命中，用于减少误杀" />
                  </a-form-item>
                  <a-form-item label="JSON字段路径存在">
                    <a-input v-model:value="rule.jsonPathsExists" :disabled="readonly" placeholder="response.error, error" />
                  </a-form-item>
                </div>

                <div class="form-grid compact">
                  <a-form-item class="wide-form-item" label="处置模板">
                    <div class="action-option-grid">
                      <button
                        v-for="template in streamInterceptActionTemplates"
                        :key="template.action"
                        class="action-option"
                        :class="{ active: rule.action === template.action }"
                        :disabled="readonly"
                        type="button"
                        @click="selectRuleAction(rule, template.action)"
                      >
                        <span class="action-option-title">
                          <span class="action-option-dot" />
                          <strong>{{ template.label }}</strong>
                          <a-tag :color="actionTagColor(template)">{{ actionTagText(template) }}</a-tag>
                        </span>
                        <span class="action-option-description">{{ template.description }}</span>
                      </button>
                    </div>
                  </a-form-item>
                </div>
                <div v-if="actionUsesTtl(rule)" class="form-grid compact">
                  <a-form-item label="避让秒数">
                    <a-input-number v-model:value="rule.avoidanceTtlSeconds" :disabled="readonly" :min="1" :max="86400" style="width: 100%" />
                  </a-form-item>
                </div>

                <a-form-item label="备注">
                  <a-textarea v-model:value="rule.notes" :disabled="readonly" :rows="1" auto-size placeholder="可写污染来源或排障线索" />
                </a-form-item>
              </div>
            </a-collapse-panel>
          </a-collapse>
        </div>
      </a-collapse-panel>
    </a-collapse>

    <a-modal v-model:open="guideOpen" title="账户流式拦截规则配置指南" width="900px" :footer="null">
      <div class="policy-guide">
        <p class="guide-note guide-intro">
          账户规则只补当前账号的 AI 对话 SSE 流拦截条件；不需要区分接口、协议或客户端类型。
        </p>

        <section class="guide-section">
          <h4>去哪里查依据</h4>
          <a-table
            :columns="guideSourceColumns"
            :data-source="streamInterceptPolicyGuideSources"
            :pagination="false"
            row-key="key"
            size="small"
          />
        </section>

        <section class="guide-section">
          <h4>字段怎么填</h4>
          <a-table
            :columns="guideFieldColumns"
            :data-source="streamInterceptPolicyGuideFields"
            :pagination="false"
            row-key="key"
            size="small"
          />
          <p class="guide-note">账户规则中，多个值用逗号、分号或换行分隔；同一个字段里的多个值是“任一命中”，不同字段之间是“同时命中”。账户规则适合处理单个中转的广告、私有错误码或异常事件格式。</p>
        </section>

        <section class="guide-section">
          <h4>处置怎么选</h4>
          <a-table
            :columns="guideActionColumns"
            :data-source="streamInterceptPolicyGuideActions"
            :pagination="false"
            row-key="key"
            size="small"
          />
        </section>

        <section class="guide-section">
          <h4>常见 SSE 事件结构</h4>
          <pre class="guide-code">{{ streamInterceptPolicyGuideExample }}</pre>
        </section>
      </div>
    </a-modal>
  </section>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { computed, ref } from 'vue'

import {
  createBlankAccountStreamInterceptRule,
  nextStreamInterceptRulePriority
} from './accountStreamInterceptPolicyOptions'
import type { AccountStreamInterceptRuleForm } from './accountStreamInterceptPolicyTypes'
import {
  streamInterceptPolicyGuideActions,
  streamInterceptPolicyGuideExample,
  streamInterceptPolicyGuideFields,
  streamInterceptPolicyGuideSources
} from '../stream-intercept-policies/streamInterceptPolicyGuide'
import {
  defaultAvoidanceTtlSeconds,
  streamInterceptActionLabel,
  streamInterceptActionTemplates,
  type StreamInterceptActionTemplate,
  streamInterceptActionUsesTtl
} from '../stream-intercept-policies/streamInterceptActionTemplates'
import type { StreamInterceptPolicyAction } from '@/types/domain'

const rules = defineModel<AccountStreamInterceptRuleForm[]>('rules', { required: true })

const props = withDefaults(defineProps<{
  readonly?: boolean
}>(), {
  readonly: false
})

const activeKeys = ref<string[]>([])
const ruleKeys = ref<string[]>([])
const guideOpen = ref(false)
const enabledRuleCount = computed(() => rules.value.filter((rule) => rule.enabled !== false).length)

const guideSourceColumns = [
  { title: '来源', key: 'name', dataIndex: 'name', width: 120 },
  { title: '查看位置', key: 'where', dataIndex: 'where' },
  { title: '说明', key: 'note', dataIndex: 'note' }
]

const guideFieldColumns = [
  { title: '字段', key: 'field', dataIndex: 'field', width: 120 },
  { title: '取值来源', key: 'source', dataIndex: 'source' },
  { title: '例子', key: 'example', dataIndex: 'example', width: 180 },
  { title: '说明', key: 'note', dataIndex: 'note' }
]

const guideActionColumns = [
  { title: '处置', key: 'action', dataIndex: 'action', width: 150 },
  { title: '适用场景', key: 'when', dataIndex: 'when' },
  { title: '说明', key: 'note', dataIndex: 'note' }
]

function ruleKey(index: number): string {
  return `stream-rule-${index}`
}

function addRule(): void {
  if (props.readonly) return
  if (!activeKeys.value.includes('stream')) {
    activeKeys.value = ['stream']
  }
  rules.value.push(createBlankAccountStreamInterceptRule(nextStreamInterceptRulePriority(rules.value)))
  ruleKeys.value = [ruleKey(rules.value.length - 1)]
}

function clearRules(): void {
  if (props.readonly) return
  rules.value = []
  ruleKeys.value = []
}

function removeRule(index: number): void {
  if (props.readonly) return
  rules.value.splice(index, 1)
  ruleKeys.value = []
}

function moveRule(index: number, offset: number): void {
  if (props.readonly) return
  const nextIndex = index + offset
  if (nextIndex < 0 || nextIndex >= rules.value.length) return
  const [rule] = rules.value.splice(index, 1)
  rules.value.splice(nextIndex, 0, rule)
  ruleKeys.value = [ruleKey(nextIndex)]
}

function expandAll(): void {
  ruleKeys.value = rules.value.map((_, index) => ruleKey(index))
}

function collapseAll(): void {
  ruleKeys.value = []
}

function handleRuleActionChange(rule: AccountStreamInterceptRuleForm): void {
  if (streamInterceptActionUsesTtl(rule.action)) {
    rule.avoidanceTtlSeconds = positiveInt(rule.avoidanceTtlSeconds) ?? defaultAvoidanceTtlSeconds
  } else {
    rule.avoidanceTtlSeconds = null
  }
}

function selectRuleAction(rule: AccountStreamInterceptRuleForm, action: StreamInterceptPolicyAction): void {
  if (props.readonly) return
  rule.action = action
  handleRuleActionChange(rule)
}

function actionUsesTtl(rule: AccountStreamInterceptRuleForm): boolean {
  return streamInterceptActionUsesTtl(rule.action)
}

function actionLabel(rule: AccountStreamInterceptRuleForm): string {
  return streamInterceptActionLabel(rule.action)
}

function actionTagText(template: StreamInterceptActionTemplate): string {
  if (template.action === 'observe') return '观察'
  if (template.action === 'drop_event' || template.action === 'fail_stream') return '不重试'
  if (template.ttlRequired) return '短期避让'
  return '重试'
}

function actionTagColor(template: StreamInterceptActionTemplate): string {
  if (template.action === 'observe') return 'gold'
  if (template.action === 'drop_event' || template.action === 'fail_stream') return 'default'
  if (template.ttlRequired) return 'orange'
  return 'green'
}

function positiveInt(value: unknown): number | undefined {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) && numberValue > 0 ? Math.trunc(numberValue) : undefined
}

function conditionSummary(rule: AccountStreamInterceptRuleForm): string {
  const parts = [
    fieldSummary('event', rule.eventTypes),
    fieldSummary('data.type', rule.dataTypes),
    fieldSummary('code', rule.errorCodes),
    fieldSummary('type', rule.errorTypes),
    fieldSummary('data文本', rule.textIncludes),
    fieldSummary('JSON路径', rule.jsonPathsExists)
  ].filter(Boolean)
  return parts.length ? parts.join('；') : '未配置匹配条件'
}

function fieldSummary(label: string, value: string): string {
  const items = value.split(/[,;，；\n]/).map((item) => item.trim()).filter(Boolean)
  return items.length ? `${label}: ${items.slice(0, 2).join(', ')}${items.length > 2 ? ` 等 ${items.length} 项` : ''}` : ''
}
</script>

<style scoped>
.stream-policy-shell {
  border: 1px solid #e9d5ff;
  border-radius: 16px;
  background: #fcfaff;
}

.stream-policy-collapse {
  border: 0;
  background: transparent;
}

.stream-policy-collapse :deep(.ant-collapse-item) {
  border-bottom: 0;
}

.stream-policy-collapse :deep(.ant-collapse-header) {
  align-items: center !important;
  padding: 12px 16px !important;
}

.stream-policy-collapse :deep(.ant-collapse-content) {
  border-top: 1px solid #ede9fe;
  background: transparent;
}

.stream-policy-collapse :deep(.ant-collapse-content-box) {
  padding: 12px 16px 16px !important;
}

.policy-summary,
.policy-content {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 10px;
}

.policy-title-row {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.policy-title-tags {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.policy-title-row h4 {
  margin: 0;
  color: #111827;
  font-size: 16px;
}

.policy-actions,
.rule-actions,
.rule-summary-main {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}

.rule-actions {
  justify-content: flex-end;
}

.rule-collapse {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.rule-collapse :deep(.ant-collapse-item) {
  overflow: hidden;
  border: 1px solid #e8edf5;
  border-radius: 12px !important;
  background: #fff;
}

.rule-collapse :deep(.ant-collapse-header) {
  align-items: flex-start !important;
  min-height: 42px;
  padding: 7px 10px !important;
}

.rule-collapse :deep(.ant-collapse-header-text) {
  min-width: 0;
}

.rule-collapse :deep(.ant-collapse-extra) {
  flex: 0 0 auto;
  margin-inline-start: 12px;
}

.rule-collapse :deep(.ant-collapse-content-box) {
  padding: 10px 12px 12px !important;
  border-top: 1px solid #eef2f7;
}

.rule-summary {
  display: flex;
  width: 100%;
  min-width: 0;
  flex-direction: column;
  align-items: stretch;
  gap: 4px;
}

.rule-summary-main {
  min-width: 0;
}

.rule-summary-main strong {
  overflow: hidden;
  max-width: 180px;
  color: #111827;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rule-condition-summary {
  display: block;
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rule-panel.disabled {
  opacity: 0.62;
}

.rule-editor {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px 12px;
}

.form-grid.compact {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.matcher-grid {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.wide-form-item {
  grid-column: 1 / -1;
}

.action-option-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

.action-option {
  display: flex;
  min-height: 78px;
  flex-direction: column;
  gap: 7px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: #fff;
  padding: 10px 12px;
  color: inherit;
  cursor: pointer;
  text-align: left;
  transition: border-color 0.16s ease, background 0.16s ease, box-shadow 0.16s ease;
}

.action-option:hover {
  border-color: #91caff;
  background: #f8fbff;
}

.action-option.active {
  border-color: #1677ff;
  background: #f0f7ff;
  box-shadow: inset 0 0 0 1px rgba(22, 119, 255, 0.18);
}

.action-option-title {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.action-option-title strong {
  overflow: hidden;
  color: #111827;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.action-option-dot {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  border: 2px solid #cbd5e1;
  border-radius: 50%;
  background: #fff;
}

.action-option.active .action-option-dot {
  border-color: #1677ff;
  box-shadow: inset 0 0 0 2px #fff;
  background: #1677ff;
}

.action-option-description {
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

.compact-empty {
  padding: 14px 0;
}

.policy-guide {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.policy-guide :deep(.ant-table-wrapper) {
  overflow-x: auto;
}

.guide-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.guide-section h4 {
  margin: 0;
  color: #0f172a;
  font-size: 14px;
}

.guide-note {
  color: #64748b;
  font-size: 12px;
  line-height: 20px;
}

.guide-intro {
  margin: 0;
}

.guide-code {
  overflow-x: auto;
  margin: 0;
  border: 1px solid #e8edf5;
  border-radius: 8px;
  background: #f8fafc;
  padding: 12px;
  color: #334155;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 20px;
}

@media (max-width: 720px) {
  .policy-title-row {
    align-items: flex-start;
    flex-direction: column;
  }

  .policy-title-tags,
  .policy-actions,
  .rule-actions {
    justify-content: flex-start;
  }

  .rule-condition-summary {
    width: 100%;
    min-width: 0;
    text-align: left;
  }

  .form-grid,
  .form-grid.compact,
  .matcher-grid,
  .action-option-grid {
    grid-template-columns: 1fr;
  }
}
</style>
