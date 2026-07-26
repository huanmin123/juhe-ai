<template>
  <a-modal :open="open" title="定时检查" width="900px" :footer="null" @update:open="emit('update:open', $event)">
    <a-alert class="schedule-alert" type="info" show-icon message="每个账户配置一条计划；检测模式与处罚规则均由本计划独立控制。" />
    <a-form :model="form" class="schedule-form" layout="vertical" @finish="save">
      <div class="schedule-basic-grid">
        <a-form-item label="检查账户" name="accountId" :rules="[{ required: true, message: '请选择检查账户' }]">
          <a-select
            v-model:value="form.accountId"
            show-search
            allow-clear
            :filter-option="false"
            :loading="accountOptionsLoading"
            :options="accountOptions"
            placeholder="输入账户名称搜索"
            @search="emit('account-search', $event)"
          />
        </a-form-item>
        <a-form-item label="检查模型" name="model" :rules="[{ required: true, message: '请选择检查模型' }]">
          <a-select v-model:value="form.model" :options="selectedModelOptions" placeholder="请选择模型" />
        </a-form-item>
        <a-form-item label="检查间隔" name="intervalMinutes" :rules="intervalRules">
          <a-input-number v-model:value="form.intervalMinutes" :min="10" :max="10080" :precision="0" addon-after="分钟" />
        </a-form-item>
        <a-form-item label="启用">
          <a-switch v-model:checked="form.enabled" />
        </a-form-item>
      </div>
      <div class="schedule-policy-grid">
        <a-form-item label="深度检测">
          <a-switch v-model:checked="deepDetection" />
          <div class="schedule-field-help">耗时更长且消耗更多 Token</div>
        </a-form-item>
        <a-form-item label="处罚阈值" name="penaltyThreshold" :rules="thresholdRules">
          <a-input-number v-model:value="form.penaltyThreshold" :min="40" :max="100" :precision="0" addon-after="分" />
        </a-form-item>
        <a-form-item label="处罚方式" name="penaltyAction" :rules="[{ required: true, message: '请选择处罚方式' }]">
          <a-select v-model:value="form.penaltyAction" :options="penaltyOptions" />
        </a-form-item>
        <a-form-item v-if="form.penaltyAction === 'quality_isolate'" label="质量隔离恢复周期" name="recoveryIntervalMinutes" :rules="recoveryRules">
          <a-input-number v-model:value="form.recoveryIntervalMinutes" :min="10" :max="10080" :precision="0" addon-after="分钟" />
        </a-form-item>
      </div>
      <div class="schedule-form-actions">
        <a-button type="primary" html-type="submit" :loading="saving">{{ form.expectedRevision ? '保存修改' : '新增计划' }}</a-button>
        <a-button v-if="form.expectedRevision" @click="resetForm">取消编辑</a-button>
      </div>
    </a-form>

    <a-divider>已配置账户</a-divider>
    <a-spin :spinning="loading">
      <a-empty v-if="!schedules.length" description="暂无定时检查计划" />
      <a-list v-else :data-source="schedules" item-layout="horizontal">
        <template #renderItem="{ item }">
          <a-list-item>
            <template #actions>
              <a-button type="link" size="small" @click="edit(item)">编辑</a-button>
              <a-popconfirm title="确认删除这条定时检查计划？" @confirm="emit('delete', item.id)">
                <a-button type="link" danger size="small">删除</a-button>
              </a-popconfirm>
            </template>
            <a-list-item-meta :title="item.accountName || item.accountId">
              <template #description>
                <a-space wrap>
                  <a-tag :color="item.enabled ? 'green' : 'default'">{{ item.enabled ? '已启用' : '已暂停' }}</a-tag>
                  <span>{{ item.model }}</span>
                  <span>每 {{ item.intervalMinutes }} 分钟</span>
                  <a-tag :color="item.profile === 'full' ? 'purple' : 'cyan'">{{ item.profile === 'full' ? '深度检测' : '快速检测' }}</a-tag>
                  <span>阈值 {{ item.penaltyThreshold }} 分</span>
                  <span>{{ penaltyActionText(item.penaltyAction) }}</span>
                  <span v-if="item.penaltyAction === 'quality_isolate'">每 {{ item.recoveryIntervalMinutes }} 分钟恢复检查</span>
                  <span>下次 {{ formatDateTime(item.nextRunAt) }}</span>
                  <span v-if="item.lastRunStatus">上次：{{ statusText(item.lastRunStatus) }}</span>
                  <a-tag v-if="item.currentEnforcementAction === 'quality_isolate'" color="red">质量隔离</a-tag>
                </a-space>
              </template>
            </a-list-item-meta>
          </a-list-item>
        </template>
      </a-list>
      <a-pagination
        v-if="total > pageSize"
        class="schedule-pagination"
        :current="page"
        :page-size="pageSize"
        :total="total"
        show-less-items
        @change="emit('page-change', $event)"
      />
    </a-spin>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { formatDateTime } from '@/shared/formatters'
import type { ModelQualityPenaltyAction, ModelQualitySchedule, ModelQualityScheduleMutationInput } from '@/types/domain'
import { statusText } from './modelCheckFormatters'

const props = defineProps<{
  accountOptions: Array<{ label: string; value: string; modelCheckModels: string[] }>
  accountOptionsLoading: boolean
  loading: boolean
  modelOptions: Array<{ label: string; value: string }>
  open: boolean
  page: number
  pageSize: number
  resetToken: number
  saving: boolean
  schedules: ModelQualitySchedule[]
  total: number
}>()
const emit = defineEmits<{
  (event: 'account-search', value: string): void
  (event: 'delete', id: string): void
  (event: 'page-change', page: number): void
  (event: 'save', value: ModelQualityScheduleMutationInput): void
  (event: 'update:open', value: boolean): void
}>()
const form = reactive<ModelQualityScheduleMutationInput>({
  accountId: '',
  model: '',
  intervalMinutes: 60,
  profile: 'quick',
  penaltyThreshold: 70,
  penaltyAction: 'fallback',
  recoveryIntervalMinutes: 10,
  enabled: true
})
const deepDetection = computed({
  get: () => form.profile === 'full',
  set: (enabled: boolean) => { form.profile = enabled ? 'full' : 'quick' }
})
const penaltyOptions: Array<{ label: string; value: ModelQualityPenaltyAction }> = [
  { label: '降级备用', value: 'fallback' },
  { label: '停用', value: 'disable' },
  { label: '质量隔离', value: 'quality_isolate' }
]
const intervalRules = [{
  validator: (_rule: unknown, value: number) => Number.isInteger(value) && value >= 10 && value <= 10080
    ? Promise.resolve()
    : Promise.reject(new Error('请输入 10 到 10080 的整数'))
}]
const thresholdRules = [{
  validator: (_rule: unknown, value: number) => Number.isInteger(value) && value >= 40 && value <= 100
    ? Promise.resolve()
    : Promise.reject(new Error('请输入 40 到 100 的整数'))
}]
const recoveryRules = [{
  validator: (_rule: unknown, value: number) => Number.isInteger(value) && value >= 10 && value <= 10080
    ? Promise.resolve()
    : Promise.reject(new Error('请输入 10 到 10080 的整数'))
}]
const selectedModelOptions = computed(() => {
  const account = props.accountOptions.find((item) => item.value === form.accountId)
  if (!account) return props.modelOptions
  return props.modelOptions.filter((item) => account.modelCheckModels.includes(item.value))
})

watch(() => props.open, (open) => { if (!open) resetForm() })
watch(() => props.resetToken, () => resetForm())
watch(() => form.accountId, () => ensureSelectedModel())
watch(() => props.accountOptions, () => ensureSelectedModel(), { deep: true })

function save() {
  if (!form.accountId || !form.model || !Number.isInteger(form.intervalMinutes) || form.intervalMinutes < 10) return
  if (!Number.isInteger(form.penaltyThreshold) || form.penaltyThreshold < 40 || form.penaltyThreshold > 100) return
  if (form.penaltyAction === 'quality_isolate' && (!Number.isInteger(form.recoveryIntervalMinutes) || form.recoveryIntervalMinutes < 10 || form.recoveryIntervalMinutes > 10080)) return
  emit('save', { ...form })
}

function edit(item: ModelQualitySchedule) {
  form.accountId = item.accountId
  form.model = item.model
  form.intervalMinutes = item.intervalMinutes
  form.profile = item.profile
  form.penaltyThreshold = item.penaltyThreshold
  form.penaltyAction = item.penaltyAction
  form.recoveryIntervalMinutes = item.recoveryIntervalMinutes
  form.enabled = item.enabled
  form.expectedRevision = item.revision
  emit('account-search', item.accountName || item.accountId)
}

function resetForm() {
  form.accountId = ''
  form.model = ''
  form.intervalMinutes = 60
  form.profile = 'quick'
  form.penaltyThreshold = 70
  form.penaltyAction = 'fallback'
  form.recoveryIntervalMinutes = 10
  form.enabled = true
  delete form.expectedRevision
}

function ensureSelectedModel() {
  const options = selectedModelOptions.value
  if (options.some((item) => item.value === form.model)) return
  form.model = options[0]?.value || ''
}

function penaltyActionText(action: ModelQualityPenaltyAction): string {
  if (action === 'disable') return '不达标时停用'
  if (action === 'quality_isolate') return '不达标时质量隔离'
  return '不达标时降级备用'
}
</script>

<style scoped>
.schedule-alert { margin-bottom: 16px; }
.schedule-form { display: grid; gap: 14px; }
.schedule-basic-grid { display: grid; grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr) minmax(180px, 0.8fr) auto; align-items: start; gap: 10px; }
.schedule-policy-grid { display: grid; grid-template-columns: minmax(150px, 0.9fr) minmax(170px, 0.9fr) minmax(180px, 1fr) minmax(210px, 1.15fr); align-items: start; gap: 10px; padding-top: 12px; border-top: 1px solid #f0f0f0; }
.schedule-form :deep(.ant-form-item) { margin-bottom: 0; min-width: 0; }
.schedule-form :deep(.ant-form-item-control) { min-height: 56px; }
.schedule-form :deep(.ant-select), .schedule-form :deep(.ant-input-number-group-wrapper) { width: 100%; }
.schedule-form-actions { display: flex; grid-column: 1 / -1; justify-content: flex-end; gap: 8px; min-width: 0; }
.schedule-field-help { margin-top: 4px; color: #8c8c8c; font-size: 12px; line-height: 1.4; }
.schedule-pagination { margin-top: 14px; text-align: right; }
@media (max-width: 900px) {
  .schedule-basic-grid, .schedule-policy-grid { grid-template-columns: 1fr; align-items: stretch; }
  .schedule-form :deep(.ant-form-item-control) { min-height: 0; }
  .schedule-form-actions { grid-column: 1; flex-direction: column; }
  .schedule-form-actions :deep(.ant-btn) { width: 100%; margin-inline-start: 0; }
}
</style>
