<template>
  <section class="error-policy-shell">
    <a-collapse v-model:activeKey="policyActiveKeys" class="error-policy-collapse" expand-icon-position="end">
      <a-collapse-panel key="policy">
        <template #header>
          <div class="policy-summary">
            <div class="policy-title">
              <h4>错误处理策略</h4>
              <p>按优先级从小到大匹配，命中第一条即停止。</p>
            </div>
            <a-tag color="blue">{{ enabledRuleCount }}/{{ rules.length }} 启用</a-tag>
          </div>
        </template>

        <div class="policy-content">
          <div class="policy-toolbar">
            <span class="policy-tip">未命中规则的未知异常会短暂重试，再临时不可调用。</span>
            <a-space class="error-policy-actions" :size="6" wrap>
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
              <a-popconfirm title="恢复默认规则会覆盖当前列表，确定继续吗？" ok-text="恢复" cancel-text="取消" @confirm="resetDefaultRules">
                <a-button size="small">恢复默认</a-button>
              </a-popconfirm>
            </a-space>
          </div>

          <a-empty v-if="rules.length === 0" class="compact-empty" description="还没有错误处理规则">
            <a-space>
              <a-button type="primary" @click="addBlankRule">添加第一条规则</a-button>
              <a-button @click="resetDefaultRules">恢复默认规则</a-button>
            </a-space>
          </a-empty>

          <a-collapse v-else v-model:activeKey="activeRuleKeys" class="rule-collapse" ghost>
            <a-collapse-panel v-for="(rule, index) in rules" :key="ruleKey(index)" class="rule-panel" :class="{ disabled: rule.enabled === false }">
              <template #header>
                <div class="rule-summary">
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
                  <span>{{ ruleConditionSummary(rule) }}</span>
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
                    <a-input-number v-model:value="rule.duration_minutes" :min="1" :max="1440" style="width: 100%" />
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
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'

import {
  accountErrorActionOptions,
  accountErrorHourOptions,
  accountErrorPolicyPresets,
  accountErrorRecoveryStrategyOptions,
  accountErrorWeekdayOptions,
  buildDefaultAccountErrorPolicyRules,
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

const rules = defineModel<AccountErrorPolicyRuleForm[]>('rules', { required: true })

const policyActiveKeys = ref<string[]>([])
const activeRuleKeys = ref<string[]>([])
const actionOptions = accountErrorActionSelectOptions
const enabledRuleCount = computed(() => rules.value.filter((rule) => rule.enabled !== false).length)

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

function resetDefaultRules() {
  rules.value = buildDefaultAccountErrorPolicyRules().map(cloneAccountErrorPolicyRule)
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
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.policy-title {
  min-width: 0;
}

.policy-title h4 {
  margin: 0;
  color: #0f172a;
  font-size: 16px;
}

.policy-title p {
  margin: 3px 0 0;
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.error-policy-actions,
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

.policy-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 32px;
}

.policy-tip {
  color: #64748b;
  font-size: 12px;
  line-height: 20px;
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
  align-items: center !important;
  min-height: 42px;
  padding: 7px 10px !important;
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
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.rule-summary strong {
  overflow: hidden;
  max-width: 180px;
  color: #0f172a;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rule-summary span {
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
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

@media (max-width: 992px) {
  .policy-summary,
  .policy-toolbar {
    flex-direction: column;
    align-items: stretch;
  }

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

