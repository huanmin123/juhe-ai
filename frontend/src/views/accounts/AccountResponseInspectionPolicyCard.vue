<template>
  <section class="response-policy-shell">
    <a-collapse v-model:activeKey="policyActiveKeys" class="response-policy-collapse" expand-icon-position="end">
      <a-collapse-panel key="policy">
        <template #header>
          <div class="policy-summary">
            <div class="policy-title-row">
              <h4 class="policy-title-text">
                <span>响应检查策略</span>
                <a-tooltip title="在上游返回 200 后继续检查内容、SSE 事件或错误字段，识别账号专属广告、污染文本或异常响应；会和全局策略一起生效。">
                  <QuestionCircleOutlined class="help-icon" />
                </a-tooltip>
              </h4>
              <a-space :size="6" wrap>
                <a-tag color="purple">{{ enabledRuleCount }}/{{ rules.length }} 启用</a-tag>
                <a-tag color="blue">叠加全局策略</a-tag>
                <a-tag v-if="readonly" color="default">只读</a-tag>
              </a-space>
            </div>
          </div>
        </template>

        <div class="policy-content">
          <a-space class="response-policy-actions" :size="6" wrap>
            <a-button size="small" :icon="h(QuestionCircleOutlined)" @click="guideOpen = true">配置指南</a-button>
            <a-button v-if="!readonly" size="small" @click="addRule">添加规则</a-button>
            <a-button size="small" :disabled="rules.length === 0" @click="expandAll">展开全部</a-button>
            <a-button size="small" :disabled="rules.length === 0" @click="collapseAll">收起全部</a-button>
            <a-button v-if="!readonly" size="small" :disabled="rules.length === 0" @click="normalizePriorities">重排优先级</a-button>
            <a-popconfirm v-if="!readonly" title="确定清空当前账户响应检查规则吗？" ok-text="清空" cancel-text="取消" @confirm="clearRules">
              <a-button size="small" :disabled="rules.length === 0">清空规则</a-button>
            </a-popconfirm>
          </a-space>

          <a-empty v-if="rules.length === 0" class="compact-empty" description="当前账户没有专属响应检查规则">
            <a-button v-if="!readonly" type="primary" @click="addRule">添加账户规则</a-button>
          </a-empty>

          <a-collapse v-else v-model:activeKey="activeRuleKeys" class="rule-collapse" ghost>
            <a-collapse-panel v-for="(rule, index) in rules" :key="ruleKey(index)" class="rule-panel" :class="{ disabled: rule.enabled === false }">
              <template #header>
                <div class="rule-summary">
                  <div class="rule-summary-main">
                    <a-switch v-model:checked="rule.enabled" size="small" checked-children="启" un-checked-children="停" :disabled="readonly" @click.stop />
                    <a-tag color="blue">P{{ rule.priority ?? '-' }}</a-tag>
                    <a-tag color="cyan">{{ responseInspectionActionLabel(rule.action) }}</a-tag>
                    <a-tag v-if="responseInspectionActionUsesRuntimeAvoidance(rule.action)" color="orange">短期避让</a-tag>
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
                    <a-input v-model:value="rule.name" :disabled="readonly" placeholder="例如 屏蔽账号专属广告" />
                  </a-form-item>
                  <a-form-item label="优先级" tooltip="小值先匹配。账户专属规则会与全局策略叠加，命中后按处置模板处理。">
                    <a-input-number v-model:value="rule.priority" :disabled="readonly" :min="1" :max="9999" style="width: 100%" />
                  </a-form-item>
                </div>

                <ResponseInspectionMatchFields :form="rule" :disabled="readonly" />

                <a-form-item label="处置模板" tooltip="命中响应检查规则后的处理方式，例如短期避让当前账号、返回本地错误或仅记录诊断。">
                  <ResponseInspectionActionSelector v-model="rule.action" :disabled="readonly" />
                </a-form-item>

                <a-form-item label="备注">
                  <a-textarea v-model:value="rule.notes" :disabled="readonly" :rows="1" auto-size placeholder="可写污染来源或排障线索" />
                </a-form-item>
              </div>
            </a-collapse-panel>
          </a-collapse>
        </div>
      </a-collapse-panel>
    </a-collapse>

    <ResponseInspectionPolicyGuideModal
      v-model:open="guideOpen"
      title="账户响应检查策略配置指南"
      intro="这里配置的是当前账号的专属规则，会和全局响应检查策略一起生效。适合处理某个账号固定返回的广告、私有错误码或异常提示。"
      match-note="多个值用英文逗号或中文逗号分隔。同一个字段填多个值时，命中任意一个就算这个字段通过；填写了多个字段时，所有字段都要通过。输出文本排除只用于减少误伤。"
    />
  </section>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { computed, h, ref } from 'vue'

import {
  createBlankAccountResponseInspectionRule,
  nextAccountResponseInspectionPriority,
  normalizeAccountResponseInspectionPriorities
} from './accountResponseInspectionPolicyRules'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import ResponseInspectionActionSelector from '../response-inspection-policies/ResponseInspectionActionSelector.vue'
import ResponseInspectionMatchFields from '../response-inspection-policies/ResponseInspectionMatchFields.vue'
import ResponseInspectionPolicyGuideModal from '../response-inspection-policies/ResponseInspectionPolicyGuideModal.vue'
import {
  responseInspectionActionLabel,
  responseInspectionActionUsesRuntimeAvoidance
} from '../response-inspection-policies/responseInspectionActionTemplates'
import {
  responseInspectionClientProfileOptions,
  responseInspectionFieldSummary
} from '../response-inspection-policies/responseInspectionPolicyForm'

const rules = defineModel<AccountResponseInspectionRuleForm[]>('rules', { required: true })

const props = withDefaults(defineProps<{
  readonly?: boolean
}>(), {
  readonly: false
})

const policyActiveKeys = ref<string[]>([])
const activeRuleKeys = ref<string[]>([])
const guideOpen = ref(false)
const enabledRuleCount = computed(() => rules.value.filter((rule) => rule.enabled !== false).length)

function openPolicy(): void {
  policyActiveKeys.value = ['policy']
}

function ruleKey(index: number): string {
  return `response-rule-${index}`
}

function addRule(): void {
  if (props.readonly) return
  openPolicy()
  rules.value.push(createBlankAccountResponseInspectionRule(nextAccountResponseInspectionPriority(rules.value)))
  activeRuleKeys.value = [ruleKey(rules.value.length - 1)]
}

function clearRules(): void {
  if (props.readonly) return
  rules.value = []
  activeRuleKeys.value = []
}

function removeRule(index: number): void {
  if (props.readonly) return
  rules.value.splice(index, 1)
  activeRuleKeys.value = []
}

function moveRule(index: number, offset: number): void {
  if (props.readonly) return
  const nextIndex = index + offset
  if (nextIndex < 0 || nextIndex >= rules.value.length) return
  const [rule] = rules.value.splice(index, 1)
  rules.value.splice(nextIndex, 0, rule)
  activeRuleKeys.value = [ruleKey(nextIndex)]
}

function normalizePriorities(): void {
  if (props.readonly) return
  rules.value = normalizeAccountResponseInspectionPriorities(rules.value)
}

function expandAll(): void {
  activeRuleKeys.value = rules.value.map((_, index) => ruleKey(index))
}

function collapseAll(): void {
  activeRuleKeys.value = []
}

function conditionSummary(rule: AccountResponseInspectionRuleForm): string {
  const parts = [
    responseInspectionFieldSummary('请求客户端', rule.clientProfiles.map(clientProfileLabel)),
    responseInspectionFieldSummary('输出', rule.outputTextIncludes),
    responseInspectionFieldSummary('排除', rule.outputTextExcludes),
    responseInspectionFieldSummary('code', rule.errorCodes),
    responseInspectionFieldSummary('type', rule.errorTypes),
    responseInspectionFieldSummary('消息', rule.errorMessageIncludes),
    responseInspectionFieldSummary('状态', rule.finishReasons),
    responseInspectionFieldSummary('JSON路径', rule.jsonPathsExists),
    responseInspectionFieldSummary('SSE 原文', rule.rawTextIncludes)
  ].filter(Boolean)
  return parts.length ? parts.join('；') : '未配置匹配条件'
}

function clientProfileLabel(value: string): string {
  return responseInspectionClientProfileOptions.find((option) => option.value === value)?.label ?? value
}
</script>

<style scoped>
.response-policy-shell {
  border: 1px solid #e9d5ff;
  border-radius: 16px;
  background: #fcfaff;
}

.response-policy-collapse {
  border: 0;
  background: transparent;
}

.response-policy-collapse :deep(.ant-collapse-item) {
  border-bottom: 0;
}

.response-policy-collapse :deep(.ant-collapse-header) {
  align-items: center !important;
  padding: 12px 16px !important;
}

.response-policy-collapse :deep(.ant-collapse-content) {
  border-top: 1px solid #ede9fe;
  background: transparent;
}

.response-policy-collapse :deep(.ant-collapse-content-box) {
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

.policy-title-row h4 {
  margin: 0;
  color: #111827;
  font-size: 16px;
}

.policy-title-text {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.help-icon {
  color: #94a3b8;
  cursor: help;
  font-size: 14px;
}

.help-icon:hover {
  color: #1677ff;
}

.response-policy-actions,
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
  gap: 2px;
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 12px;
}

.form-grid.compact {
  grid-template-columns: repeat(2, minmax(0, 1fr));
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

@media (max-width: 720px) {
  .policy-title-row {
    align-items: flex-start;
    flex-direction: column;
  }

  .response-policy-actions,
  .rule-actions {
    justify-content: flex-start;
  }

  .form-grid,
  .form-grid.compact {
    grid-template-columns: 1fr;
  }
}
</style>
