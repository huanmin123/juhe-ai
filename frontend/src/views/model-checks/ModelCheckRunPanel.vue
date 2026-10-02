<template>
  <a-card class="page-card model-checks-run-card">
    <a-form class="model-checks-form" layout="vertical">
      <div class="model-checks-control-panel model-checks-control-desktop">
        <div class="model-checks-fields">
          <a-form-item v-if="isManagementView" class="model-checks-system-account-field" required>
            <SystemPrincipalSelect
              :value="systemAccountFilter"
              :selected-principal="systemAccountFilterSelection"
              :accounts="systemAccounts"
              :active-only="false"
              include-all
              allow-clear
              :disabled="submitting || !targetId"
              :filter-option="false"
              :loading="systemAccountOptionsLoading"
              placeholder="请选择系统账户"
              @change="emit('system-account-change')"
              @dropdown-visible-change="emit('system-account-dropdown-visible-change', $event)"
              @search="emit('system-account-search', $event)"
              @update:selected-principal="emit('update:systemAccountFilterSelection', $event)"
              @update:value="handleSystemAccountValueUpdate"
            />
          </a-form-item>
          <a-form-item class="model-checks-account-field" required>
            <AccountSelect
              :value="targetId"
              :selected-account="selectedTargetAccount"
              show-search
              allow-clear
              :disabled="accountSelectDisabled"
              :filter-option="false"
              :loading="targetOptionsLoading"
              :options="targetOptions"
              :placeholder="accountSelectPlaceholder"
              @change="handleTargetChange"
              @dropdown-visible-change="emit('target-dropdown-visible-change', $event)"
              @search="emit('target-search', $event)"
              @update:selected-account="emit('update:selectedTargetAccount', $event)"
              @update:value="emit('target-value-update', $event)"
            />
          </a-form-item>
          <a-form-item class="model-checks-model-field" required>
            <a-select
              :value="model"
              :options="modelOptions"
              :loading="modelOptionsLoading"
              :disabled="submitting"
              placeholder="模型"
              @dropdown-visible-change="emit('model-dropdown-visible-change', $event)"
              @update:value="handleModelValueUpdate"
            />
          </a-form-item>
          <a-form-item class="model-checks-comparison-field">
            <AccountSelect
              :value="trustedComparisonAccountId"
              :selected-account="selectedComparisonAccount"
              show-search
              allow-clear
              :disabled="comparisonSelectDisabled"
              :filter-option="false"
              :loading="comparisonOptionsLoading"
              :options="comparisonOptions"
              :placeholder="comparisonSelectPlaceholder"
              @dropdown-visible-change="emit('comparison-dropdown-visible-change', $event)"
              @search="emit('comparison-search', $event)"
              @update:selected-account="emit('update:selectedComparisonAccount', $event)"
              @update:value="handleComparisonValueUpdate"
            />
          </a-form-item>
          <a-button :loading="optionsLoading" @click="emit('refresh', true)">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
          <a-button :disabled="submitting" @click="emit('reset')">重置</a-button>
        </div>

        <div class="model-checks-toolbar">
          <ModelQualityConfigPopover
            :disabled="qualityActionsDisabled"
            :is-management-view="isManagementView"
            :loading="qualityPolicyLoading"
            :policy="qualityPolicy"
            :saving="qualityPolicySaving"
            @open="emit('quality-policy-open')"
            @save="emit('quality-policy-save', $event)"
          />
          <a-button :disabled="qualityActionsDisabled" @click="emit('question-bank-open')">
            <template #icon><BookOutlined /></template>
            {{ isManagementView ? '题库管理' : '题库添加' }}
          </a-button>
          <a-button type="primary" :loading="submitting" @click="emit('submit')">
            <template #icon>
              <ExperimentOutlined />
            </template>
            {{ deepDetection ? '开始深度检测' : '快速检测' }}
          </a-button>
          <a-button :disabled="qualityActionsDisabled" @click="emit('schedules-open')">
            <template #icon><ClockCircleOutlined /></template>
            定时检查
          </a-button>
        </div>
      </div>

      <!-- 手机端紧凑条：当前条件摘要 + 快速检测常驻，条件与工具收进抽屉 -->
      <div class="model-checks-mobile-bar">
        <button type="button" class="model-checks-mobile-summary" @click="mobileConditionsOpen = true">
          <span class="model-checks-mobile-summary-main">{{ mobileConditionSummary }}</span>
          <span class="model-checks-mobile-summary-hint">检测条件</span>
        </button>
        <a-button type="primary" :loading="submitting" @click="emit('submit')">
          <template #icon>
            <ExperimentOutlined />
          </template>
          {{ deepDetection ? '开始深度检测' : '快速检测' }}
        </a-button>
      </div>
    </a-form>

    <a-drawer
      v-model:open="mobileConditionsOpen"
      title="检测条件"
      placement="bottom"
      height="min(72vh, 560px)"
      class="model-checks-mobile-drawer"
      :body-style="{ padding: '14px 16px 16px' }"
    >
      <div class="model-checks-mobile-body">
        <div v-if="isManagementView" class="model-checks-mobile-field">
          <span class="model-checks-mobile-label">系统账户</span>
          <SystemPrincipalSelect
            :value="systemAccountFilter"
            :selected-principal="systemAccountFilterSelection"
            :accounts="systemAccounts"
            :active-only="false"
            include-all
            allow-clear
            :disabled="submitting || !targetId"
            :filter-option="false"
            :loading="systemAccountOptionsLoading"
            placeholder="请选择系统账户"
            class="model-checks-mobile-control"
            @change="emit('system-account-change')"
            @dropdown-visible-change="emit('system-account-dropdown-visible-change', $event)"
            @search="emit('system-account-search', $event)"
            @update:selected-principal="emit('update:systemAccountFilterSelection', $event)"
            @update:value="handleSystemAccountValueUpdate"
          />
        </div>
        <div class="model-checks-mobile-field">
          <span class="model-checks-mobile-label">检测账户</span>
          <AccountSelect
            :value="targetId"
            :selected-account="selectedTargetAccount"
            show-search
            allow-clear
            :disabled="accountSelectDisabled"
            :filter-option="false"
            :loading="targetOptionsLoading"
            :options="targetOptions"
            :placeholder="accountSelectPlaceholder"
            class="model-checks-mobile-control"
            @change="handleTargetChange"
            @dropdown-visible-change="emit('target-dropdown-visible-change', $event)"
            @search="emit('target-search', $event)"
            @update:selected-account="emit('update:selectedTargetAccount', $event)"
            @update:value="emit('target-value-update', $event)"
          />
        </div>
        <div class="model-checks-mobile-field">
          <span class="model-checks-mobile-label">检查模型</span>
          <a-select
            :value="model"
            :options="modelOptions"
            :loading="modelOptionsLoading"
            :disabled="submitting"
            placeholder="模型"
            class="model-checks-mobile-control"
            @dropdown-visible-change="emit('model-dropdown-visible-change', $event)"
            @update:value="handleModelValueUpdate"
          />
        </div>
        <div class="model-checks-mobile-field">
          <span class="model-checks-mobile-label">可信对比账户（可选）</span>
          <AccountSelect
            :value="trustedComparisonAccountId"
            :selected-account="selectedComparisonAccount"
            show-search
            allow-clear
            :disabled="comparisonSelectDisabled"
            :filter-option="false"
            :loading="comparisonOptionsLoading"
            :options="comparisonOptions"
            :placeholder="comparisonSelectPlaceholder"
            class="model-checks-mobile-control"
            @dropdown-visible-change="emit('comparison-dropdown-visible-change', $event)"
            @search="emit('comparison-search', $event)"
            @update:selected-account="emit('update:selectedComparisonAccount', $event)"
            @update:value="handleComparisonValueUpdate"
          />
        </div>
        <div class="model-checks-mobile-row">
          <a-button block :loading="optionsLoading" @click="emit('refresh', true)">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
          <a-button block :disabled="submitting" @click="emit('reset')">重置</a-button>
        </div>
        <div class="model-checks-mobile-row">
          <ModelQualityConfigPopover
            :disabled="qualityActionsDisabled"
            :is-management-view="isManagementView"
            :loading="qualityPolicyLoading"
            :policy="qualityPolicy"
            :saving="qualityPolicySaving"
            @open="emit('quality-policy-open')"
            @save="emit('quality-policy-save', $event)"
          />
          <a-button :disabled="qualityActionsDisabled" @click="emit('question-bank-open')">
            <template #icon><BookOutlined /></template>
            {{ isManagementView ? '题库管理' : '题库添加' }}
          </a-button>
          <a-button :disabled="qualityActionsDisabled" @click="emit('schedules-open')">
            <template #icon><ClockCircleOutlined /></template>
            定时检查
          </a-button>
        </div>
        <a-button type="primary" block :loading="submitting" @click="emit('submit')">
          <template #icon>
            <ExperimentOutlined />
          </template>
          {{ deepDetection ? '开始深度检测' : '快速检测' }}
        </a-button>
      </div>
    </a-drawer>

    <ModelCheckTerminal
      :lines="terminalLines"
      :status-color="terminalStatusColor"
      :status-text="terminalStatusText"
      :submitting="submitting"
      :visible="terminalVisible"
      :waiting-text="terminalWaitingText"
      @stop="emit('stop')"
    />
  </a-card>
</template>

<script setup lang="ts">
import { BookOutlined, ClockCircleOutlined, ExperimentOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { computed, ref } from 'vue'

import AccountSelect from '@/components/AccountSelect.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { AccountSelection, SelectOption } from '@/shared/accountLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { ModelCheckModel, ModelQualityPolicy, ModelQualityPolicyUpdateInput, SystemAccountPrincipalSummary } from '@/types/domain'
import ModelCheckTerminal, { type ModelCheckTerminalLine } from './ModelCheckTerminal.vue'
import ModelQualityConfigPopover from './ModelQualityConfigPopover.vue'

type SelectValue = string | string[] | undefined

const props = defineProps<{
  accountSelectDisabled: boolean
  comparisonSelectDisabled: boolean
  accountSelectPlaceholder: string
  comparisonOptions: SelectOption[]
  comparisonOptionsLoading: boolean
  comparisonSelectPlaceholder: string
  deepDetection: boolean
  isManagementView: boolean
  model: ModelCheckModel
  modelOptions: Array<{ label: string; value: string }>
  modelOptionsLoading: boolean
  optionsLoading: boolean
  qualityPolicy: ModelQualityPolicy
  qualityActionsDisabled: boolean
  qualityPolicyLoading: boolean
  qualityPolicySaving: boolean
  selectedComparisonAccount?: AccountSelection
  selectedTargetAccount?: AccountSelection
  submitting: boolean
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
  systemAccountOptionsLoading: boolean
  systemAccounts: SystemAccountPrincipalSummary[]
  targetId?: string
  targetOptions: SelectOption[]
  targetOptionsLoading: boolean
  terminalLines: ModelCheckTerminalLine[]
  terminalStatusColor: string
  terminalStatusText: string
  terminalVisible: boolean
  terminalWaitingText: string
  trustedComparisonAccountId?: string
}>()

const emit = defineEmits<{
  (event: 'comparison-dropdown-visible-change', open: boolean): void
  (event: 'comparison-search', value: string): void
  (event: 'model-dropdown-visible-change', open: boolean): void
  (event: 'question-bank-open'): void
  (event: 'refresh', force?: boolean): void
  (event: 'reset'): void
  (event: 'quality-policy-open'): void
  (event: 'quality-policy-save', value: ModelQualityPolicyUpdateInput): void
  (event: 'schedules-open'): void
  (event: 'stop'): void
  (event: 'submit'): void
  (event: 'system-account-change'): void
  (event: 'system-account-dropdown-visible-change', open: boolean): void
  (event: 'system-account-search', value: string): void
  (event: 'target-change', value: SelectValue, option: unknown): void
  (event: 'target-dropdown-visible-change', open: boolean): void
  (event: 'target-search', value: string): void
  (event: 'target-value-update', value: SelectValue): void
  (event: 'update:model', value: ModelCheckModel): void
  (event: 'update:selectedComparisonAccount', value?: AccountSelection): void
  (event: 'update:selectedTargetAccount', value?: AccountSelection): void
  (event: 'update:systemAccountFilter', value?: string): void
  (event: 'update:systemAccountFilterSelection', value?: PrincipalSelection): void
  (event: 'update:trustedComparisonAccountId', value?: string): void
}>()

// 手机端：条件抽屉开合；紧凑条摘要显示当前检测账户/模型。
const mobileConditionsOpen = ref(false)
const mobileConditionSummary = computed(() => {
  const accountLabel = props.selectedTargetAccount?.name
  const modelLabel = props.modelOptions.find((option) => option.value === props.model)?.label ?? props.model
  if (accountLabel && modelLabel) return `${accountLabel} · ${modelLabel}`
  if (modelLabel) return modelLabel
  return accountLabel ?? '未选择检测条件'
})

function handleSystemAccountValueUpdate(value: SelectValue) {
  emit('update:systemAccountFilter', selectStringValue(value))
}

function handleTargetChange(value: SelectValue, option: unknown) {
  emit('target-change', value, option)
}

function handleModelValueUpdate(value: SelectValue) {
  if (typeof value === 'string') {
    emit('update:model', value as ModelCheckModel)
  }
}

function handleComparisonValueUpdate(value: SelectValue) {
  emit('update:trustedComparisonAccountId', selectStringValue(value))
}

function selectStringValue(value: SelectValue): string | undefined {
  return typeof value === 'string' ? value : undefined
}
</script>

<style scoped>
.model-checks-run-card {
  flex: 0 0 auto;
  border: 1px solid var(--juhe-border);
  border-radius: 16px;
}

.model-checks-form :deep(.ant-form-item) {
  margin-bottom: 0;
}

.model-checks-control-panel {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}

.model-checks-fields {
  display: flex;
  flex: 1 1 620px;
  flex-wrap: wrap;
  gap: 12px;
  align-items: center;
  min-width: 0;
}

.model-checks-system-account-field,
.model-checks-account-field {
  flex: 0 1 300px;
  width: 300px;
  min-width: 240px;
}

.model-checks-model-field {
  flex: 0 0 160px;
  width: 160px;
  min-width: 140px;
}

.model-checks-comparison-field {
  flex: 0 1 300px;
  width: 300px;
  min-width: 240px;
}

.model-checks-toolbar {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  flex-wrap: wrap;
}

/* 手机端紧凑条（桌面隐藏）：条件摘要 + 快速检测 */
.model-checks-mobile-bar {
  display: none;
}

.model-checks-mobile-summary {
  flex: 1 1 auto;
  min-width: 0;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 1px;
  padding: 4px 0;
  border: 0;
  background: transparent;
  text-align: left;
  cursor: pointer;
}

.model-checks-mobile-summary-main {
  max-width: 100%;
  overflow: hidden;
  color: var(--juhe-fg, var(--juhe-fg));
  font-size: 13px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-checks-mobile-summary-hint {
  color: var(--juhe-muted, var(--juhe-accent));
  font-size: 11px;
}

.model-checks-mobile-bar :deep(.ant-btn) {
  flex: 0 0 auto;
}

.model-checks-mobile-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.model-checks-mobile-label {
  color: var(--juhe-muted);
  font-size: 12px;
}

.model-checks-mobile-control {
  width: 100%;
}

.model-checks-mobile-row {
  display: flex;
  gap: 10px;
  align-items: center;
}

.model-checks-mobile-row .ant-btn {
  flex: 1 1 0;
}

@media (max-width: 900px) {
  .model-checks-control-desktop {
    display: none;
  }

  .model-checks-mobile-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    min-width: 0;
  }
}

@media (min-width: 901px) {
  .model-checks-mobile-drawer {
    display: none;
  }
}
</style>
