<template>
  <a-modal
    :open="open"
    width="960px"
    wrap-class-name="model-quality-schedule-modal-wrap"
    :footer="null"
    @update:open="emit('update:open', $event)"
  >
    <template #title>
      <div class="schedule-modal-title">
        <span>定时检查</span>
        <small>自动验证账户模型质量，并按规则处理不达标结果</small>
      </div>
    </template>

    <div class="schedule-modal-content">
      <a-form :model="form" class="schedule-form" layout="vertical" @finish="save">
        <section ref="scheduleEditorRef" class="schedule-editor">
          <header class="schedule-editor-head">
            <div>
              <h3>{{ form.expectedRevision ? '编辑检查计划' : '创建检查计划' }}</h3>
              <p>每个账户仅保留一条计划，检测配置和处理规则彼此独立。</p>
            </div>
            <a-tag v-if="form.expectedRevision" color="blue">正在编辑</a-tag>
          </header>

          <div class="schedule-form-section">
            <div class="schedule-section-label">检查对象</div>
            <div class="schedule-target-grid">
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
            </div>
          </div>

          <div class="schedule-form-section">
            <div class="schedule-section-label">运行规则</div>
            <div class="schedule-rule-grid">
              <a-form-item label="检查间隔" name="intervalMinutes" :rules="intervalRules">
                <a-input-number v-model:value="form.intervalMinutes" :min="10" :max="10080" :precision="0" addon-after="分钟" />
              </a-form-item>
              <a-form-item class="schedule-profile-field" label="检测模式">
                <a-radio-group v-model:value="form.profile" button-style="solid">
                  <a-radio-button value="quick">快速检测</a-radio-button>
                  <a-radio-button value="full">深度检测</a-radio-button>
                </a-radio-group>
                <div class="schedule-field-help">
                  {{ form.profile === 'full' ? '覆盖更多能力项，耗时更长且消耗更多 Token' : '适合高频巡检，优先验证核心能力' }}
                </div>
              </a-form-item>
              <a-form-item class="schedule-state-field" label="计划状态">
                <div class="schedule-switch-row">
                  <div>
                    <strong>{{ form.enabled ? '启用计划' : '暂停计划' }}</strong>
                    <span>{{ form.enabled ? '保存后按间隔自动执行' : '保留配置但不自动执行' }}</span>
                  </div>
                  <a-switch v-model:checked="form.enabled" />
                </div>
              </a-form-item>
            </div>
          </div>

          <div class="schedule-form-section">
            <div class="schedule-section-label">不达标处理</div>
            <div class="schedule-policy-grid">
              <a-form-item label="触发阈值" name="penaltyThreshold" :rules="thresholdRules">
                <a-input-number v-model:value="form.penaltyThreshold" :min="40" :max="100" :precision="0" addon-after="分" />
              </a-form-item>
              <a-form-item label="处理方式" name="penaltyAction" :rules="[{ required: true, message: '请选择处理方式' }]">
                <a-select v-model:value="form.penaltyAction" :options="penaltyOptions" />
              </a-form-item>
              <a-form-item
                v-if="form.penaltyAction === 'quality_isolate'"
                label="恢复检查间隔"
                name="recoveryIntervalMinutes"
                :rules="recoveryRules"
              >
                <a-input-number v-model:value="form.recoveryIntervalMinutes" :min="10" :max="10080" :precision="0" addon-after="分钟" />
              </a-form-item>
              <div v-else class="schedule-policy-hint">
                检测得分低于 {{ form.penaltyThreshold }} 分时，{{ penaltyActionHint(form.penaltyAction) }}。
              </div>
            </div>
          </div>

          <footer class="schedule-form-actions">
            <a-button v-if="form.expectedRevision" @click="resetForm">取消编辑</a-button>
            <a-button type="primary" html-type="submit" :loading="saving">
              {{ form.expectedRevision ? '保存修改' : '创建计划' }}
            </a-button>
          </footer>
        </section>
      </a-form>

      <section class="schedule-list-section">
        <header class="schedule-list-head">
          <div>
            <h3>已配置计划</h3>
            <p>共 {{ total }} 条，按账户管理自动检测状态。</p>
          </div>
        </header>

        <a-spin :spinning="loading">
          <a-empty
            v-if="!schedules.length"
            class="schedule-empty"
            :image="simpleEmptyImage"
            description="还没有定时检查计划"
          >
            <span class="schedule-empty-help">完成上方配置后，点击“创建计划”即可开始自动巡检。</span>
          </a-empty>
          <div v-else class="schedule-list">
            <div class="schedule-list-columns" aria-hidden="true">
              <span>账户</span>
              <span>检查频率</span>
              <span>下次运行</span>
              <span>不达标处理</span>
              <span>上次结果</span>
              <span>操作</span>
            </div>
            <article v-for="item in schedules" :key="item.id" class="schedule-item" :class="{ 'schedule-item-disabled': !item.enabled }">
              <div class="schedule-item-identity">
                <div class="schedule-item-title-row">
                  <h4>{{ item.accountName || item.accountId }}</h4>
                  <a-tag :color="item.enabled ? 'green' : 'default'">{{ item.enabled ? '运行中' : '已暂停' }}</a-tag>
                  <a-tag v-if="item.currentEnforcementAction === 'quality_isolate'" color="red">质量隔离中</a-tag>
                </div>
                <div class="schedule-item-subtitle">
                  <span v-if="item.providerCode">{{ item.providerCode }}</span>
                  <span class="schedule-item-model">{{ item.model }}</span>
                  <span>{{ item.profile === 'full' ? '深度检测' : '快速检测' }}</span>
                </div>
              </div>
              <div class="schedule-metric">
                <span>检查频率</span>
                <strong>每 {{ item.intervalMinutes }} 分钟</strong>
              </div>
              <div class="schedule-metric">
                <span>下次运行</span>
                <strong>{{ item.enabled ? formatDateTime(item.nextRunAt) : '计划已暂停' }}</strong>
              </div>
              <div class="schedule-metric">
                <span>不达标处理</span>
                <strong>低于 {{ item.penaltyThreshold }} 分 · {{ penaltyActionText(item.penaltyAction) }}</strong>
                <small v-if="item.penaltyAction === 'quality_isolate'">每 {{ item.recoveryIntervalMinutes }} 分钟恢复检查</small>
              </div>
              <div class="schedule-metric">
                <span>上次结果</span>
                <strong>{{ lastRunText(item) }}</strong>
                <small v-if="item.lastRunAt">{{ formatDateTime(item.lastRunAt) }}</small>
              </div>
              <RowActions class="schedule-item-actions" :actions="scheduleActions" @action-click="handleScheduleAction($event, item)" />
            </article>
          </div>

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
      </section>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { Empty } from 'ant-design-vue'
import { computed, nextTick, reactive, ref, watch } from 'vue'

import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
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
const scheduleEditorRef = ref<HTMLElement>()
const simpleEmptyImage = Empty.PRESENTED_IMAGE_SIMPLE
const scheduleActions: RowActionItem[] = [
  { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
  { key: 'delete', label: '删除', icon: 'delete', tone: 'danger', confirmTitle: '确认删除这条定时检查计划？' }
]
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
  void nextTick(() => scheduleEditorRef.value?.scrollIntoView({ block: 'start' }))
}

function handleScheduleAction(action: string, item: ModelQualitySchedule) {
  if (action === 'edit') edit(item)
  if (action === 'delete') emit('delete', item.id)
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
  if (action === 'disable') return '停用账户'
  if (action === 'quality_isolate') return '质量隔离'
  return '降级为备用'
}

function penaltyActionHint(action: ModelQualityPenaltyAction): string {
  if (action === 'disable') return '系统将停用该账户'
  if (action === 'quality_isolate') return '账户将进入质量隔离'
  return '账户将降级为备用'
}

function lastRunText(item: ModelQualitySchedule): string {
  return item.lastRunStatus ? statusText(item.lastRunStatus) : '尚未运行'
}
</script>

<style scoped>
.schedule-modal-title {
  display: grid;
  gap: 3px;
}

.schedule-modal-title > span {
  color: #0f172a;
  font-size: 17px;
  font-weight: 600;
  line-height: 24px;
}

.schedule-modal-title small {
  color: #64748b;
  font-size: 12px;
  font-weight: 400;
  line-height: 18px;
}

.schedule-modal-content {
  display: grid;
  gap: 0;
}

.schedule-editor {
  min-width: 0;
}

.schedule-editor-head,
.schedule-list-head,
.schedule-item-title-row,
.schedule-item-subtitle,
.schedule-switch-row {
  display: flex;
  align-items: center;
}

.schedule-editor-head {
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding: 0 0 14px;
}

.schedule-editor h3,
.schedule-list-head h3,
.schedule-item h4 {
  margin: 0;
  color: #0f172a;
}

.schedule-editor h3,
.schedule-list-head h3 {
  font-size: 15px;
  font-weight: 600;
  line-height: 22px;
}

.schedule-editor-head p,
.schedule-list-head p {
  margin: 3px 0 0;
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

.schedule-form-section {
  display: grid;
  grid-template-columns: 112px minmax(0, 1fr);
  gap: 16px;
  padding: 16px 0 0;
  border-top: 1px solid #eef2f7;
}

.schedule-section-label {
  padding-top: 30px;
  color: #475569;
  font-size: 13px;
  font-weight: 600;
  line-height: 20px;
}

.schedule-target-grid,
.schedule-rule-grid,
.schedule-policy-grid {
  display: grid;
  align-items: start;
  gap: 12px;
}

.schedule-target-grid {
  grid-template-columns: minmax(0, 1.25fr) minmax(0, 1fr);
}

.schedule-rule-grid {
  grid-template-columns: minmax(150px, .7fr) minmax(250px, 1.15fr) minmax(210px, 1fr);
}

.schedule-policy-grid {
  grid-template-columns: minmax(150px, .75fr) minmax(220px, 1fr) minmax(220px, 1fr);
}

.schedule-form :deep(.ant-form-item) {
  min-width: 0;
  margin-bottom: 0;
}

.schedule-form :deep(.ant-form-item-control) {
  min-height: 60px;
}

.schedule-form :deep(.ant-select),
.schedule-form :deep(.ant-input-number-group-wrapper),
.schedule-form :deep(.ant-radio-group) {
  width: 100%;
}

.schedule-profile-field :deep(.ant-radio-button-wrapper) {
  width: 50%;
  padding-inline: 10px;
  text-align: center;
}

.schedule-field-help,
.schedule-policy-hint {
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

.schedule-field-help {
  margin-top: 5px;
}

.schedule-switch-row {
  justify-content: space-between;
  gap: 12px;
  min-height: 32px;
}

.schedule-switch-row > div {
  display: grid;
  min-width: 0;
  gap: 1px;
}

.schedule-switch-row strong {
  color: #334155;
  font-size: 13px;
  font-weight: 500;
  line-height: 18px;
}

.schedule-switch-row span {
  color: #94a3b8;
  font-size: 11px;
  line-height: 16px;
}

.schedule-policy-hint {
  align-self: start;
  margin-top: 30px;
  padding-top: 6px;
}

.schedule-form-actions {
  display: flex;
  grid-column: 1 / -1;
  justify-content: flex-end;
  gap: 8px;
  min-width: 0;
  margin-top: 4px;
  padding: 14px 0 0;
  border-top: 1px solid #eef2f7;
}

.schedule-list-section {
  min-width: 0;
  margin-top: 24px;
  padding-top: 20px;
  border-top: 1px solid #e2e8f0;
}

.schedule-list-head {
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}

.schedule-list {
  border-top: 1px solid #e2e8f0;
}

.schedule-list-columns,
.schedule-item {
  display: grid;
  grid-template-columns: minmax(190px, 1.25fr) minmax(90px, .65fr) minmax(150px, 1fr) minmax(170px, 1.15fr) minmax(100px, .75fr) 64px;
  gap: 14px;
}

.schedule-list-columns {
  align-items: center;
  padding: 8px 0;
  border-bottom: 1px solid #e2e8f0;
  color: #94a3b8;
  font-size: 11px;
  line-height: 16px;
}

.schedule-list-columns span:last-child {
  text-align: right;
}

.schedule-item {
  position: relative;
  align-items: center;
  padding: 14px 0;
  border-bottom: 1px solid #eef2f7;
  transition: background-color .2s ease;
}

.schedule-item:hover {
  background: #fafcff;
}

.schedule-item-disabled {
  opacity: .7;
}

.schedule-item-identity {
  min-width: 0;
}

.schedule-item-title-row,
.schedule-item-subtitle {
  flex-wrap: wrap;
  gap: 6px;
}

.schedule-item h4 {
  min-width: 0;
  overflow: hidden;
  font-size: 14px;
  font-weight: 600;
  line-height: 22px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.schedule-item-subtitle {
  margin-top: 5px;
  color: #64748b;
  font-size: 12px;
  line-height: 20px;
}

.schedule-item-subtitle > span + span::before {
  margin-right: 6px;
  color: #cbd5e1;
  content: '·';
}

.schedule-item-model {
  min-width: 0;
  overflow: hidden;
  color: #334155;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.schedule-metric {
  display: grid;
  min-width: 0;
  align-content: start;
  gap: 3px;
}

.schedule-metric span,
.schedule-metric small {
  color: #94a3b8;
  font-size: 11px;
  line-height: 16px;
}

.schedule-metric > span {
  display: none;
}

.schedule-metric strong {
  min-width: 0;
  overflow: hidden;
  color: #334155;
  font-size: 12px;
  font-weight: 500;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.schedule-empty {
  margin: 0;
  padding: 28px 16px 20px;
  border-top: 1px solid #e2e8f0;
}

.schedule-empty :deep(.ant-empty-description) {
  margin-bottom: 2px;
  color: #475569;
}

.schedule-empty-help {
  display: block;
  color: #94a3b8;
  font-size: 12px;
  line-height: 18px;
}

.schedule-pagination {
  margin-top: 14px;
  text-align: right;
}

:global(.model-quality-schedule-modal-wrap .ant-modal) {
  top: 32px;
  max-width: calc(100vw - 48px);
  padding-bottom: 32px;
}

:global(.model-quality-schedule-modal-wrap .ant-modal-content) {
  overflow: hidden;
  padding: 0;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  box-shadow: 0 24px 64px rgba(15, 23, 42, .18);
}

:global(.model-quality-schedule-modal-wrap .ant-modal-header) {
  margin: 0;
  padding: 18px 22px 15px;
  border-bottom: 1px solid #e8eef6;
}

:global(.model-quality-schedule-modal-wrap .ant-modal-body) {
  max-height: calc(100dvh - 146px);
  overflow-y: auto;
  padding: 18px 22px 22px;
}

:global(.model-quality-schedule-modal-wrap .ant-modal-close) {
  top: 17px;
  inset-inline-end: 18px;
  color: #64748b;
}

@media (max-width: 940px) {
  .schedule-form-section {
    grid-template-columns: 1fr;
    gap: 8px;
  }

  .schedule-section-label {
    padding-top: 0;
  }

  .schedule-rule-grid,
  .schedule-policy-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .schedule-state-field,
  .schedule-policy-hint {
    grid-column: 1 / -1;
  }

  .schedule-policy-hint {
    margin-top: 0;
  }

  .schedule-list-columns {
    display: none;
  }

  .schedule-item {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 14px 20px;
    align-items: start;
  }

  .schedule-item-identity {
    grid-column: 1 / -1;
    padding-right: 76px;
  }

  .schedule-item-actions {
    position: absolute;
    top: 14px;
    right: 0;
  }

  .schedule-metric > span {
    display: block;
  }
}

@media (max-width: 640px) {
  .schedule-modal-title small {
    max-width: calc(100vw - 100px);
  }

  .schedule-modal-content {
    gap: 20px;
  }

  .schedule-target-grid,
  .schedule-rule-grid,
  .schedule-policy-grid {
    grid-template-columns: 1fr;
  }

  .schedule-form :deep(.ant-form-item-control) {
    min-height: 0;
  }

  .schedule-state-field,
  .schedule-policy-hint {
    grid-column: auto;
  }

  .schedule-policy-hint {
    margin-top: 0;
  }

  .schedule-form-actions :deep(.ant-btn) {
    flex: 1 1 0;
    min-height: 36px;
  }

  .schedule-item {
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 12px;
    padding: 14px 0;
  }

  .schedule-item-identity {
    grid-column: 1;
    padding-right: 0;
  }

  .schedule-item-actions {
    position: static;
    grid-column: 2;
    grid-row: 1;
    align-self: start;
  }

  .schedule-metric {
    grid-column: 1 / -1;
  }

  .schedule-metric strong {
    white-space: normal;
  }

  :global(.model-quality-schedule-modal-wrap .ant-modal) {
    top: 12px;
    max-width: calc(100vw - 24px);
    padding-bottom: 12px;
  }

  :global(.model-quality-schedule-modal-wrap .ant-modal-header) {
    padding: 16px 18px 13px;
  }

  :global(.model-quality-schedule-modal-wrap .ant-modal-body) {
    max-height: calc(100dvh - 112px);
    padding: 14px 14px 18px;
  }
}
</style>
