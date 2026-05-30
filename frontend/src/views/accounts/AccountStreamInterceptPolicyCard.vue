<template>
  <section class="stream-policy-shell">
    <a-collapse v-model:activeKey="activeKeys" class="stream-policy-collapse" expand-icon-position="end">
      <a-collapse-panel key="stream">
        <template #header>
          <div class="policy-summary">
            <div class="policy-title-row">
              <h4>账户流式拦截规则</h4>
              <a-space :size="6">
                <a-tag color="purple">{{ enabledRuleCount }}/{{ rules.length }} 启用</a-tag>
                <a-tag color="blue">继承内置和管理端策略</a-tag>
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
            <a-button size="small" @click="addRule">添加规则</a-button>
            <a-button size="small" :disabled="rules.length === 0" @click="expandAll">展开全部</a-button>
            <a-button size="small" :disabled="rules.length === 0" @click="collapseAll">收起全部</a-button>
            <a-popconfirm title="确定清空当前账户追加规则吗？" ok-text="清空" cancel-text="取消" @confirm="clearRules">
              <a-button size="small" :disabled="rules.length === 0">清空规则</a-button>
            </a-popconfirm>
          </a-space>

          <a-empty v-if="rules.length === 0" class="compact-empty" description="当前账户没有追加流式拦截规则">
            <a-button type="primary" @click="addRule">添加账户规则</a-button>
          </a-empty>

          <a-collapse v-else v-model:activeKey="ruleKeys" class="rule-collapse" ghost>
            <a-collapse-panel v-for="(rule, index) in rules" :key="ruleKey(index)" class="rule-panel" :class="{ disabled: rule.enabled === false }">
              <template #header>
                <div class="rule-summary">
                  <div class="rule-summary-main">
                    <a-switch v-model:checked="rule.enabled" size="small" checked-children="启" un-checked-children="停" @click.stop />
                    <a-tag color="blue">P{{ rule.priority ?? '-' }}</a-tag>
                    <a-tag :color="rule.executionMode === 'dry_run' ? 'gold' : 'purple'">{{ rule.executionMode === 'dry_run' ? '试运行' : '拦截' }}</a-tag>
                    <a-tag :color="rule.retryEnabled ? 'green' : 'default'">{{ rule.retryEnabled ? '重试' : '不重试' }}</a-tag>
                    <strong>{{ rule.name || '未命名规则' }}</strong>
                  </div>
                  <span class="rule-condition-summary">{{ conditionSummary(rule) }}</span>
                </div>
              </template>

              <template #extra>
                <a-space class="rule-actions" wrap @click.stop>
                  <a-button size="small" :disabled="index === 0" @click="moveRule(index, -1)">上移</a-button>
                  <a-button size="small" :disabled="index === rules.length - 1" @click="moveRule(index, 1)">下移</a-button>
                  <a-button size="small" danger @click="removeRule(index)">删除</a-button>
                </a-space>
              </template>

              <div class="rule-editor">
                <div class="form-grid compact">
                  <a-form-item label="规则名称">
                    <a-input v-model:value="rule.name" placeholder="例如 屏蔽某中转广告" />
                  </a-form-item>
                  <a-form-item label="优先级">
                    <a-input-number v-model:value="rule.priority" :min="1" :max="9999" style="width: 100%" />
                  </a-form-item>
                  <a-form-item label="执行模式">
                    <a-select v-model:value="rule.executionMode" :options="streamInterceptExecutionModeOptions" />
                  </a-form-item>
                </div>

                <div class="form-grid matcher-grid">
                  <a-form-item label="SSE event 类型">
                    <a-input v-model:value="rule.eventTypes" placeholder="response.failed, message" />
                  </a-form-item>
                  <a-form-item label="data.type">
                    <a-input v-model:value="rule.dataTypes" placeholder="response.output_text.delta" />
                  </a-form-item>
                  <a-form-item label="error.code">
                    <a-input v-model:value="rule.errorCodes" placeholder="cyber_policy" />
                  </a-form-item>
                  <a-form-item label="error.type">
                    <a-input v-model:value="rule.errorTypes" placeholder="server_error" />
                  </a-form-item>
                  <a-form-item label="文本包含">
                    <a-textarea v-model:value="rule.textIncludes" :rows="1" auto-size placeholder="多个关键词用逗号、分号或换行分隔" />
                  </a-form-item>
                  <a-form-item label="文本不包含">
                    <a-textarea v-model:value="rule.textExcludes" :rows="1" auto-size placeholder="减少误杀时填写" />
                  </a-form-item>
                  <a-form-item label="JSON 字段存在">
                    <a-input v-model:value="rule.jsonPathsExists" placeholder="response.error, error" />
                  </a-form-item>
                </div>

                <div class="form-grid compact">
                  <a-form-item label="数据处理">
                    <a-select v-model:value="rule.dataHandling" :options="dataHandlingOptions(rule)" @change="normalizeRule(rule)" />
                  </a-form-item>
                  <a-form-item label="是否重试">
                    <a-switch v-model:checked="rule.retryEnabled" checked-children="是" un-checked-children="否" @change="normalizeRule(rule)" />
                  </a-form-item>
                  <a-form-item label="是否切号">
                    <a-select v-model:value="rule.accountSwitch" :options="accountSwitchOptions(rule)" />
                  </a-form-item>
                  <a-form-item label="账户状态">
                    <a-select v-model:value="rule.accountState" :options="streamInterceptAccountStateOptions" />
                  </a-form-item>
                  <a-form-item label="避让秒数">
                    <a-input-number v-model:value="rule.avoidanceTtlSeconds" :min="1" :max="86400" style="width: 100%" />
                  </a-form-item>
                </div>

                <a-form-item label="备注">
                  <a-textarea v-model:value="rule.notes" :rows="1" auto-size placeholder="可写污染来源或排障线索" />
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
          <p class="guide-note">多个值用逗号、分号或换行分隔；同一个字段里的多个值是“任一命中”，不同字段之间是“同时命中”。账户规则适合处理单个中转的广告、私有错误码或异常事件格式。</p>
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
  nextStreamInterceptRulePriority,
  streamInterceptAccountStateOptions,
  streamInterceptAccountSwitchOptions,
  streamInterceptDataHandlingOptions,
  streamInterceptExecutionModeOptions
} from './accountStreamInterceptPolicyOptions'
import type { AccountStreamInterceptRuleForm } from './accountStreamInterceptPolicyTypes'
import {
  streamInterceptPolicyGuideActions,
  streamInterceptPolicyGuideExample,
  streamInterceptPolicyGuideFields,
  streamInterceptPolicyGuideSources
} from '../stream-intercept-policies/streamInterceptPolicyGuide'

const rules = defineModel<AccountStreamInterceptRuleForm[]>('rules', { required: true })

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
  if (!activeKeys.value.includes('stream')) {
    activeKeys.value = ['stream']
  }
  rules.value.push(createBlankAccountStreamInterceptRule(nextStreamInterceptRulePriority(rules.value)))
  ruleKeys.value = [ruleKey(rules.value.length - 1)]
}

function clearRules(): void {
  rules.value = []
  ruleKeys.value = []
}

function removeRule(index: number): void {
  rules.value.splice(index, 1)
  ruleKeys.value = []
}

function moveRule(index: number, offset: number): void {
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

function normalizeRule(rule: AccountStreamInterceptRuleForm): void {
  if (rule.retryEnabled && rule.dataHandling === 'discard_event') {
    rule.dataHandling = 'discard_stream'
  }
  if (!rule.retryEnabled && rule.accountSwitch === 'request_next_account') {
    rule.accountSwitch = 'none'
  }
}

function dataHandlingOptions(rule: AccountStreamInterceptRuleForm) {
  return rule.retryEnabled
    ? streamInterceptDataHandlingOptions.filter((option) => option.value !== 'discard_event')
    : streamInterceptDataHandlingOptions
}

function accountSwitchOptions(rule: AccountStreamInterceptRuleForm) {
  return rule.retryEnabled
    ? streamInterceptAccountSwitchOptions
    : streamInterceptAccountSwitchOptions.filter((option) => option.value !== 'request_next_account')
}

function conditionSummary(rule: AccountStreamInterceptRuleForm): string {
  const parts = [
    fieldSummary('event', rule.eventTypes),
    fieldSummary('data.type', rule.dataTypes),
    fieldSummary('code', rule.errorCodes),
    fieldSummary('type', rule.errorTypes),
    fieldSummary('文本', rule.textIncludes),
    fieldSummary('字段', rule.jsonPathsExists)
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
  border-radius: 8px;
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

.policy-title-row,
.rule-summary {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
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

.rule-condition-summary {
  min-width: 160px;
  overflow: hidden;
  color: #64748b;
  text-align: right;
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
  .policy-title-row,
  .rule-summary {
    align-items: flex-start;
    flex-direction: column;
  }

  .rule-condition-summary {
    width: 100%;
    min-width: 0;
    text-align: left;
  }

  .form-grid,
  .form-grid.compact,
  .matcher-grid {
    grid-template-columns: 1fr;
  }
}
</style>
