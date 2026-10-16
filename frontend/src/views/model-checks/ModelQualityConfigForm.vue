<template>
  <a-spin :spinning="loading">
    <div v-if="scopeLocked" class="quality-config">
      <a-alert
        type="info"
        show-icon
        message="请先在顶部选择具体系统账户"
        description="手动检测质量配置按系统账户作用域读写。选定系统账户后即可在这里配置深度检测、手动测试处罚与题库测试。"
      />
    </div>
    <div v-else class="quality-config">
      <div>
        <div class="quality-config-title">手动检测质量配置</div>
        <div class="quality-config-help">仅用于页面手动检查；定时计划使用各自独立配置。</div>
      </div>
      <div class="quality-config-row">
        <div>
          <div class="quality-config-label">深度检测</div>
          <div class="quality-config-help">开启后手动检查使用深度模式；耗时更长且消耗更多 Token，请按需开启。</div>
        </div>
        <a-switch v-model:checked="form.deepDetection" :disabled="saving" />
      </div>
      <div class="quality-config-row">
        <div>
          <div class="quality-config-label">手动测试处罚</div>
          <div class="quality-config-help">开启后，手动检测不达标会按下方方式处罚自有账户；关闭后仅记录检测结果与健康状态，不修改账户。</div>
        </div>
        <a-switch v-model:checked="form.manualEnforcementEnabled" :disabled="saving" />
      </div>
      <div class="quality-config-row">
        <div>
          <div class="quality-config-label">超时处罚</div>
          <div class="quality-config-help">开启后，检测环节超时（重试耗尽仍无完整响应）按该环节失败计分；核心环节超时导致整轮不可用时直接按下方处罚方式执行。默认关闭，超时不处罚。</div>
        </div>
        <a-switch v-model:checked="form.timeoutPenaltyEnabled" :disabled="saving" />
      </div>
      <label class="quality-config-field">
        <span>处罚阈值</span>
        <a-input-number v-model:value="form.penaltyThreshold" :min="40" :max="100" :precision="0" :disabled="saving" addon-after="分" />
      </label>
      <label class="quality-config-field">
        <span>处罚方式</span>
        <a-select v-model:value="form.penaltyAction" :disabled="saving" :options="penaltyOptions" />
      </label>
      <label v-if="form.penaltyAction === 'quality_isolate'" class="quality-config-field">
        <span>质量隔离恢复周期</span>
        <a-input-number v-model:value="form.recoveryIntervalMinutes" :min="10" :max="10080" :precision="0" :disabled="saving" addon-after="分钟" />
      </label>
      <label class="quality-config-field">
        <span>题库测试（选填，最多 3 题）</span>
        <QuestionBankSelect v-model:value="form.customQuestionIds" :disabled="saving" :is-management-view="isManagementView" />
        <span class="quality-config-help">选择已通过的题库题目，检测最后逐题作答并按 31 分池扣分。</span>
      </label>
      <div class="quality-config-actions">
        <a-button :disabled="saving" @click="handleCancel">取消</a-button>
        <a-button type="primary" :loading="saving" @click="save">保存</a-button>
      </div>
    </div>
  </a-spin>
</template>

<script setup lang="ts">
import { reactive, watch } from 'vue'

import type { ModelQualityPenaltyAction, ModelQualityPolicy, ModelQualityPolicyUpdateInput } from '@/types/domain'
import QuestionBankSelect from './QuestionBankSelect.vue'

const props = defineProps<{
  isManagementView: boolean
  loading: boolean
  policy: ModelQualityPolicy
  // 管理视图下未选定具体系统账户时为 true：表单整体换成引导提示
  // （质量配置按系统账户作用域读写，不能落到"全部账户"）。
  scopeLocked?: boolean
  saving: boolean
}>()
const emit = defineEmits<{
  (event: 'cancel'): void
  (event: 'save', value: ModelQualityPolicyUpdateInput): void
}>()
const form = reactive({
  deepDetection: false,
  manualEnforcementEnabled: true,
  timeoutPenaltyEnabled: false,
  penaltyThreshold: 70,
  penaltyAction: 'fallback' as ModelQualityPenaltyAction,
  recoveryIntervalMinutes: 10,
  customQuestionIds: [] as string[]
})
const penaltyOptions = [
  { label: '降级备用', value: 'fallback' },
  { label: '停用', value: 'disable' },
  { label: '质量隔离', value: 'quality_isolate' }
]

function syncFromPolicy(policy: ModelQualityPolicy) {
  form.deepDetection = policy.profile === 'full'
  form.manualEnforcementEnabled = policy.manualEnforcementEnabled
  form.timeoutPenaltyEnabled = policy.timeoutPenaltyEnabled
  form.penaltyThreshold = policy.penaltyThreshold
  form.penaltyAction = policy.penaltyAction
  form.recoveryIntervalMinutes = policy.recoveryIntervalMinutes
  form.customQuestionIds = [...(policy.customQuestionIds ?? [])]
}

watch(() => props.policy, syncFromPolicy, { immediate: true, deep: true })

function handleCancel() {
  syncFromPolicy(props.policy)
  emit('cancel')
}

function save() {
  emit('save', {
    expectedRevision: props.policy.revision,
    profile: form.deepDetection ? 'full' : 'quick',
    manualEnforcementEnabled: form.manualEnforcementEnabled,
    timeoutPenaltyEnabled: form.timeoutPenaltyEnabled,
    penaltyThreshold: Math.trunc(form.penaltyThreshold),
    penaltyAction: form.penaltyAction,
    recoveryIntervalMinutes: Math.trunc(form.recoveryIntervalMinutes),
    customQuestionIds: [...form.customQuestionIds]
  })
}
</script>

<style scoped>
/* 隐式 auto 轨道的最小尺寸是内容的 min-content，题库长标题 tag（不可断行）
   会把整个网格撑出 360px 弹层；显式 minmax(0,1fr) 钉死轨道可收缩。 */
.quality-config { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; }
.quality-config-title, .quality-config-label { color: var(--juhe-fg); font-weight: 600; }
.quality-config-title { font-size: 14px; }
.quality-config-help { margin-top: 3px; color: var(--juhe-muted); font-size: 12px; line-height: 1.5; }
.quality-config-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.quality-config-row > div:first-child { min-width: 0; }
.quality-config-row :deep(.ant-switch) { flex-shrink: 0; }
.quality-config-field { display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; color: var(--juhe-fg-soft); font-size: 13px; font-weight: 600; }
.quality-config-field :deep(.ant-select), .quality-config-field :deep(.ant-input-number-group-wrapper) { width: 100%; }
.quality-config-actions { display: flex; justify-content: flex-end; gap: 8px; }
</style>
