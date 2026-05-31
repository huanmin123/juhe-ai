<template>
  <section class="error-policy-shell">
    <a-collapse v-model:activeKey="policyActiveKeys" class="error-policy-collapse" expand-icon-position="end">
      <a-collapse-panel key="policy">
        <template #header>
          <div class="policy-summary">
            <div class="policy-title-row">
              <h4>错误处理策略</h4>
              <a-tag color="blue">{{ enabledRuleCount }}/{{ rules.length }} 启用</a-tag>
            </div>
          </div>
        </template>

        <div class="policy-content">
          <a-space class="error-policy-actions" :size="6" wrap>
            <a-button size="small" :icon="h(QuestionCircleOutlined)" @click="guideOpen = true">配置指南</a-button>
            <a-dropdown>
              <a-button size="small">添加预设</a-button>
              <template #overlay>
                <a-menu @click="handlePresetClick">
                  <a-menu-item v-for="preset in accountErrorPolicyPresets" :key="preset.key">{{ preset.label }}</a-menu-item>
                </a-menu>
              </template>
            </a-dropdown>
            <a-button size="small" @click="addBlankRule">自定义规则</a-button>
            <a-button size="small" @click="expandAllRules">展开全部</a-button>
            <a-button size="small" @click="collapseAllRules">收起全部</a-button>
            <a-button size="small" @click="normalizePriorities">重排优先级</a-button>
            <a-popconfirm title="清空后账号只走通用失败处理，确定继续吗？" ok-text="清空" cancel-text="取消" @confirm="clearRules">
              <a-button size="small" :disabled="rules.length === 0">清空规则</a-button>
            </a-popconfirm>
          </a-space>

          <a-empty v-if="rules.length === 0" class="compact-empty" :description="contextGuide.emptyDescription">
            <a-space>
              <a-button type="primary" @click="addBlankRule">添加专属规则</a-button>
            </a-space>
          </a-empty>

          <a-collapse v-else v-model:activeKey="activeRuleKeys" class="rule-collapse" ghost>
            <a-collapse-panel v-for="(rule, index) in rules" :key="ruleKey(index)" class="rule-panel" :class="{ disabled: rule.enabled === false }">
              <template #header>
                <div class="rule-summary">
                  <div class="rule-summary-main">
                    <a-switch
                      v-model:checked="rule.enabled"
                      size="small"
                      checked-children="启"
                      un-checked-children="停"
                      @click.stop
                    />
                    <a-tag class="priority-tag" color="blue">P{{ rule.priority ?? '-' }}</a-tag>
                    <a-tag :color="actionColor(rule.action)">{{ actionLabel(rule.action) }}</a-tag>
                    <strong>{{ rule.name || '未命名规则' }}</strong>
                  </div>
                  <span class="rule-condition-summary">{{ ruleConditionSummary(rule) }}</span>
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
                <div class="form-grid error-rule-grid compact">
                  <a-form-item label="规则名称">
                    <a-input v-model:value="rule.name" placeholder="例如 429 临时限流" />
                  </a-form-item>
                  <a-form-item label="优先级">
                    <a-input-number v-model:value="rule.priority" :min="1" :max="9999" style="width: 100%" />
                  </a-form-item>
                  <a-form-item label="处理动作">
                    <a-select v-model:value="rule.action" :options="actionOptions" />
                  </a-form-item>
                </div>

                <div class="form-grid error-rule-grid matcher-grid">
                  <a-form-item label="状态码">
                    <a-input v-model:value="rule.status_codes" placeholder="429, 502, 503" />
                  </a-form-item>
                  <a-form-item label="错误码">
                    <a-input v-model:value="rule.error_codes" placeholder="insufficient_user_quota" />
                  </a-form-item>
                  <a-form-item label="错误类型">
                    <a-input v-model:value="rule.error_types" placeholder="new_api_error" />
                  </a-form-item>
                  <a-form-item label="关键词">
                    <a-textarea v-model:value="rule.keywords" :rows="1" auto-size placeholder="多个关键词用逗号、分号或换行分隔" />
                  </a-form-item>
                </div>

                <div v-if="rule.action === 'temp_unschedulable'" class="form-grid error-rule-grid compact">
                  <a-form-item label="临时避让分钟数">
                    <a-input-number v-model:value="rule.durationMinutes" :min="1" :max="1440" style="width: 100%" />
                  </a-form-item>
                </div>

                <div v-else-if="rule.action === 'rate_limited'" class="form-grid error-rule-grid compact">
                  <a-form-item label="恢复策略">
                    <a-select v-model:value="rule.reset_strategy" :options="accountErrorRecoveryStrategyOptions" />
                  </a-form-item>
                  <a-form-item v-if="rule.reset_strategy === 'duration'" label="恢复小时数">
                    <a-input-number v-model:value="rule.duration_hours" :min="1" :max="720" style="width: 100%" />
                  </a-form-item>
                  <a-form-item v-if="rule.reset_strategy === 'daily'" label="每天恢复时间">
                    <a-select v-model:value="rule.daily_reset_hour" :options="accountErrorHourOptions" />
                  </a-form-item>
                  <a-form-item v-if="rule.reset_strategy === 'weekly'" label="每周恢复日">
                    <a-select v-model:value="rule.weekly_reset_day" :options="accountErrorWeekdayOptions" />
                  </a-form-item>
                  <a-form-item v-if="rule.reset_strategy === 'weekly'" label="每周恢复时间">
                    <a-select v-model:value="rule.weekly_reset_hour" :options="accountErrorHourOptions" />
                  </a-form-item>
                </div>

                <a-form-item label="说明">
                  <a-textarea v-model:value="rule.description" :rows="1" auto-size placeholder="可写为什么要这样处理" />
                </a-form-item>
              </div>
            </a-collapse-panel>
          </a-collapse>
        </div>
      </a-collapse-panel>
    </a-collapse>

    <a-modal v-model:open="guideOpen" title="错误处理策略配置指南" width="860px" :footer="null">
      <div class="policy-guide">
        <section class="guide-section">
          <h4>去哪里查错误</h4>
          <a-table
            :columns="guideSourceColumns"
            :data-source="accountErrorPolicyGuideSources"
            :pagination="false"
            row-key="key"
            size="small"
          />
        </section>

        <section class="guide-section">
          <h4>字段怎么填</h4>
          <a-table
            :columns="guideFieldColumns"
            :data-source="accountErrorPolicyGuideFields"
            :pagination="false"
            row-key="key"
            size="small"
          />
          <p class="guide-note">多个值用逗号、分号或换行分隔；同一个字段里的多个值是“任一命中”，不同字段之间是“同时命中”。</p>
        </section>

        <section class="guide-section">
          <h4>常见响应结构</h4>
          <pre class="guide-code">{
  "error": {
    "message": "可读错误说明",
    "type": "错误类型，填到错误类型",
    "code": "错误码，填到错误码"
  }
}</pre>
        </section>
      </div>
    </a-modal>
  </section>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { computed, h, ref } from 'vue'

import {
  accountErrorActionOptions,
  accountErrorHourOptions,
  accountErrorPolicyPresets,
  accountErrorRecoveryStrategyOptions,
  accountErrorWeekdayOptions,
  cloneAccountErrorPolicyRule,
  createBlankAccountErrorRule,
  getNextAccountErrorRulePriority,
  normalizeAccountErrorPolicyPriorities,
  type AccountErrorPolicyRuleForm
} from './accountErrorPolicy'
import {
  accountErrorActionColor as actionColor,
  accountErrorActionLabel as actionLabel,
  accountErrorActionSelectOptions,
  accountErrorRuleConditionSummary as ruleConditionSummary,
  accountErrorRuleKey as ruleKey
} from './accountErrorPolicyDisplay'
import {
  accountErrorPolicyGuideFields,
  accountErrorPolicyGuideSources,
  resolveAccountErrorPolicyContextGuide
} from './accountErrorPolicyGuide'

const rules = defineModel<AccountErrorPolicyRuleForm[]>('rules', { required: true })

const props = withDefaults(defineProps<{
  accountType?: string
  baseUrl?: string
  providerCode?: string
}>(), {
  accountType: '',
  baseUrl: '',
  providerCode: ''
})

const policyActiveKeys = ref<string[]>([])
const activeRuleKeys = ref<string[]>([])
const guideOpen = ref(false)
const actionOptions = accountErrorActionSelectOptions
const enabledRuleCount = computed(() => rules.value.filter((rule) => rule.enabled !== false).length)
const contextGuide = computed(() => resolveAccountErrorPolicyContextGuide({
  accountType: props.accountType,
  baseUrl: props.baseUrl,
  providerCode: props.providerCode
}))

const guideSourceColumns = [
  { title: '来源', key: 'name', dataIndex: 'name', width: 120 },
  { title: '查看位置', key: 'where', dataIndex: 'where' },
  { title: '说明', key: 'note', dataIndex: 'note' }
]

const guideFieldColumns = [
  { title: '字段', key: 'field', dataIndex: 'field', width: 100 },
  { title: '取值来源', key: 'source', dataIndex: 'source' },
  { title: '例子', key: 'example', dataIndex: 'example', width: 180 },
  { title: '说明', key: 'note', dataIndex: 'note' }
]

function openPolicy() {
  policyActiveKeys.value = ['policy']
}

function addBlankRule() {
  openPolicy()
  rules.value.push(createBlankAccountErrorRule(getNextAccountErrorRulePriority(rules.value)))
  activeRuleKeys.value = [ruleKey(rules.value.length - 1)]
}

function handlePresetClick(event: { key: string | number }) {
  const preset = accountErrorPolicyPresets.find((item) => item.key === String(event.key))
  if (!preset) return
  openPolicy()
  rules.value.push(cloneAccountErrorPolicyRule({
    ...preset.rule,
    priority: getNextAccountErrorRulePriority(rules.value)
  }))
  activeRuleKeys.value = [ruleKey(rules.value.length - 1)]
}

function clearRules() {
  rules.value = []
  activeRuleKeys.value = []
}

function removeRule(index: number) {
  rules.value.splice(index, 1)
  activeRuleKeys.value = []
}

function moveRule(index: number, offset: number) {
  const nextIndex = index + offset
  if (nextIndex < 0 || nextIndex >= rules.value.length) return
  const [rule] = rules.value.splice(index, 1)
  rules.value.splice(nextIndex, 0, rule)
  activeRuleKeys.value = [ruleKey(nextIndex)]
}

function normalizePriorities() {
  rules.value = normalizeAccountErrorPolicyPriorities(rules.value)
}

function expandAllRules() {
  activeRuleKeys.value = rules.value.map((_, index) => ruleKey(index))
}

function collapseAllRules() {
  activeRuleKeys.value = []
}
</script>

<style scoped>
.error-policy-shell {
  border: 1px solid #dbeafe;
  border-radius: 16px;
  background: linear-gradient(180deg, #f8fbff 0%, #ffffff 100%);
}

.error-policy-collapse {
  border: 0;
  background: transparent;
}

.error-policy-collapse :deep(.ant-collapse-item) {
  border-bottom: 0;
}

.error-policy-collapse :deep(.ant-collapse-header) {
  align-items: center !important;
  padding: 12px 16px !important;
}

.error-policy-collapse :deep(.ant-collapse-content) {
  border-top: 1px solid #e8edf5;
  background: transparent;
}

.error-policy-collapse :deep(.ant-collapse-content-box) {
  padding: 12px 16px 16px !important;
}

.policy-summary {
  display: flex;
  width: 100%;
  min-width: 0;
  flex-direction: column;
  align-items: stretch;
  gap: 8px;
}

.policy-title-row {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.policy-title-row h4 {
  margin: 0;
  color: #0f172a;
  font-size: 16px;
}

.error-policy-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-start;
  gap: 6px;
}

.rule-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 6px;
}

.policy-content {
  display: flex;
  flex-direction: column;
  gap: 10px;
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

.rule-panel.disabled {
  opacity: 0.72;
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
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.rule-summary-main strong {
  overflow: hidden;
  max-width: 180px;
  color: #0f172a;
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

.priority-tag {
  margin-inline-end: 0;
}

.rule-editor {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 12px;
}

.error-rule-grid.compact {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.matcher-grid {
  grid-template-columns: repeat(4, minmax(0, 1fr));
}

.rule-editor :deep(.ant-form-item) {
  margin-bottom: 10px;
}

.rule-editor :deep(.ant-form-item-label) {
  padding-bottom: 2px;
}

.compact-empty {
  padding: 12px 0;
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

@media (max-width: 992px) {
  .error-policy-actions,
  .rule-actions {
    justify-content: flex-start;
  }

  .form-grid,
  .error-rule-grid.compact,
  .matcher-grid {
    grid-template-columns: 1fr;
  }

}
</style>

