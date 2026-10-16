<template>
  <a-card class="page-card model-checks-run-card">
    <a-form class="model-checks-form" layout="vertical">
      <div class="model-checks-control-panel model-checks-control-desktop">
        <div class="model-checks-condition-summary">
          <span class="model-checks-mobile-summary-main">{{ conditionSummary }}</span>
        </div>

        <div class="model-checks-toolbar">
          <a-button :disabled="submitting" @click="emit('question-bank-open')">
            <template #icon><BookOutlined /></template>
            {{ isManagementView ? '题库管理' : '题库添加' }}
          </a-button>
          <a-button :disabled="submitting" @click="emit('schedules-open')">
            <template #icon><ClockCircleOutlined /></template>
            定时检查
          </a-button>
          <a-button type="primary" :disabled="submitting" @click="openDesktopConfig">
            <template #icon>
              <ExperimentOutlined />
            </template>
            检测
          </a-button>
        </div>
      </div>

      <!-- 手机端紧凑条：当前条件摘要 + 快速检测常驻，条件与工具收进抽屉 -->
      <div class="model-checks-mobile-bar">
        <button type="button" class="model-checks-mobile-summary" @click="mobileConditionsOpen = true">
          <span class="model-checks-mobile-summary-main">{{ conditionSummary }}</span>
          <span class="model-checks-mobile-summary-hint">检测条件</span>
        </button>
        <a-button type="primary" :loading="submitting" @click="emit('submit')">
          <template #icon>
            <ExperimentOutlined />
          </template>
          检测
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
            :disabled="modelSelectDisabled"
            :placeholder="modelSelectPlaceholder"
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
            :disabled="submitting"
            :is-management-view="isManagementView"
            :loading="qualityPolicyLoading"
            :policy="qualityPolicy"
            :saving="qualityPolicySaving"
            :scope-locked="qualityScopeLocked"
            @open="emit('quality-policy-open')"
            @save="emit('quality-policy-save', $event)"
          />
          <a-button :disabled="submitting" @click="emit('question-bank-open')">
            <template #icon><BookOutlined /></template>
            {{ isManagementView ? '题库管理' : '题库添加' }}
          </a-button>
          <a-button :disabled="submitting" @click="emit('schedules-open')">
            <template #icon><ClockCircleOutlined /></template>
            定时检查
          </a-button>
        </div>
        <a-button type="primary" block :loading="submitting" @click="emit('submit')">
          <template #icon>
            <ExperimentOutlined />
          </template>
          检测
        </a-button>
      </div>
    </a-drawer>

    <!-- 桌面端检测配置抽屉：点「快速检测」打开，配置齐全后在抽屉内触发；布局样式与手机条件抽屉共用 -->
    <a-drawer
      v-model:open="desktopConfigOpen"
      title="发起模型检测"
      placement="right"
      :width="480"
      class="model-checks-desktop-config-drawer"
    >
      <div class="model-checks-drawer-fields">
        <div v-if="isManagementView" class="model-checks-mobile-field">
          <span class="model-checks-mobile-label">系统账户</span>
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
            :disabled="modelSelectDisabled"
            :placeholder="modelSelectPlaceholder"
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
        <ModelQualityConfigForm
          class="model-checks-drawer-quality"
          :is-management-view="isManagementView"
          :loading="qualityPolicyLoading"
          :policy="qualityPolicy"
          :saving="qualityPolicySaving"
          :scope-locked="qualityScopeLocked"
          @save="emit('quality-policy-save', $event)"
        />
      </div>
      <template #footer>
        <div class="model-checks-drawer-footer">
          <a-button :loading="optionsLoading" @click="emit('refresh', true)">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
          <a-button :disabled="submitting" @click="emit('reset')">重置</a-button>
          <a-button
            type="primary"
            :loading="submitting"
            :disabled="!desktopSubmitReady"
            @click="handleDesktopSubmit"
          >
            开始检测
          </a-button>
        </div>
      </template>
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
import ModelQualityConfigForm from './ModelQualityConfigForm.vue'
import ModelQualityConfigPopover from './ModelQualityConfigPopover.vue'

type SelectValue = string | string[] | undefined

const props = defineProps<{
  accountSelectDisabled: boolean
  comparisonSelectDisabled: boolean
  accountSelectPlaceholder: string
  comparisonOptions: SelectOption[]
  comparisonOptionsLoading: boolean
  comparisonSelectPlaceholder: string
  isManagementView: boolean
  model?: ModelCheckModel
  modelOptions: Array<{ label: string; value: string }>
  modelOptionsLoading: boolean
  optionsLoading: boolean
  qualityPolicy: ModelQualityPolicy
  qualityScopeLocked: boolean
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

// 手机端：条件抽屉开合；桌面端：点「快速检测」打开配置抽屉。
const mobileConditionsOpen = ref(false)
const desktopConfigOpen = ref(false)
// 模型下拉收敛为账户支持模型：未选检测账户时禁用；已选账户但账户模型能力
// 未按需加载时保持可打开并提示加载中（选项为空，打开下拉触发补拉）。
const modelSelectDisabled = computed(() => props.submitting || !props.targetId?.trim())
const modelSelectPlaceholder = computed(() => {
  if (!props.targetId?.trim()) return '请先选择检测账户'
  if (props.modelOptionsLoading) return '正在加载该账户支持的模型'
  return '模型'
})
const conditionSummary = computed(() => {
  const accountLabel = props.selectedTargetAccount?.name
  const modelLabel = props.modelOptions.find((option) => option.value === props.model)?.label ?? props.model
  if (accountLabel && modelLabel) return `${accountLabel} · ${modelLabel}`
  if (modelLabel) return modelLabel
  return accountLabel ?? '尚未配置检测条件'
})
// 抽屉内「开始检测」的前置条件：检测账户与模型均已选定（父组件 submit 仍有兜底校验）。
const desktopSubmitReady = computed(() => Boolean(props.targetId?.trim()) && Boolean(props.model))

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

// 桌面抽屉内触发检测后立即关闭，进度与结果在主页面终端区和历史列表查看。
function handleDesktopSubmit() {
  emit('submit')
  desktopConfigOpen.value = false
}

// 打开抽屉即按当前系统账户作用域拉取最新质量策略，供抽屉内平铺的配置表单读写。
function openDesktopConfig() {
  desktopConfigOpen.value = true
  emit('quality-policy-open')
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

.model-checks-condition-summary {
  min-width: 0;
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 1px;
  padding: 4px 0;
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

/* 桌面端检测配置抽屉：字段竖排复用 mobile-field 布局样式 */
.model-checks-drawer-fields {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.model-checks-drawer-quality {
  padding-top: 14px;
  border-top: 1px solid var(--juhe-border);
}

.model-checks-drawer-footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
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
