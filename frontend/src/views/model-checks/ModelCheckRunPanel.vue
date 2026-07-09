<template>
  <a-card class="page-card model-checks-run-card">
    <a-form class="model-checks-form" layout="vertical">
      <div class="model-checks-control-panel">
        <div class="model-checks-fields">
          <a-form-item v-if="isManagementView" class="model-checks-system-account-field" required>
            <SystemPrincipalSelect
              :value="systemAccountFilter"
              :selected-principal="systemAccountFilterSelection"
              :accounts="systemAccounts"
              :active-only="false"
              include-all
              allow-clear
              :disabled="submitting"
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
              :loading="optionsLoading"
              :disabled="submitting"
              placeholder="模型"
              @update:value="handleModelValueUpdate"
            />
          </a-form-item>
          <a-form-item class="model-checks-comparison-field">
            <AccountSelect
              :value="trustedComparisonAccountId"
              :selected-account="selectedComparisonAccount"
              show-search
              allow-clear
              :disabled="accountSelectDisabled"
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
          <a-button :loading="optionsLoading" @click="emit('refresh')">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
          <a-button :disabled="submitting" @click="emit('reset')">重置</a-button>
        </div>

        <div class="model-checks-toolbar">
          <a-button type="primary" :loading="submitting" @click="emit('submit')">
            <template #icon>
              <ExperimentOutlined />
            </template>
            开始检测
          </a-button>
        </div>
      </div>
    </a-form>

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
import { ExperimentOutlined, ReloadOutlined } from '@ant-design/icons-vue'

import AccountSelect from '@/components/AccountSelect.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { AccountSelection, SelectOption } from '@/shared/accountLabelCache'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { ModelCheckModel, SystemAccountPrincipalSummary } from '@/types/domain'
import ModelCheckTerminal, { type ModelCheckTerminalLine } from './ModelCheckTerminal.vue'

type SelectValue = string | string[] | undefined

defineProps<{
  accountSelectDisabled: boolean
  accountSelectPlaceholder: string
  comparisonOptions: SelectOption[]
  comparisonOptionsLoading: boolean
  comparisonSelectPlaceholder: string
  isManagementView: boolean
  model: ModelCheckModel
  modelOptions: Array<{ label: string; value: string }>
  optionsLoading: boolean
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
  (event: 'refresh'): void
  (event: 'reset'): void
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
  border: 1px solid #e8edf5;
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

@media (max-width: 900px) {
  .model-checks-control-panel,
  .model-checks-fields {
    width: 100%;
    flex-direction: column;
    align-items: stretch;
  }

  .model-checks-fields {
    flex: none;
  }

  .model-checks-system-account-field,
  .model-checks-account-field,
  .model-checks-model-field,
  .model-checks-comparison-field {
    width: 100%;
    flex: none;
    min-width: 0;
  }

  .model-checks-toolbar {
    align-items: stretch;
    width: 100%;
    flex-direction: column;
    justify-content: flex-start;
  }

  .model-checks-fields :deep(.ant-btn),
  .model-checks-toolbar :deep(.ant-btn) {
    width: 100%;
  }
}
</style>
