<template>
  <a-popover v-model:open="open" trigger="click" placement="bottomRight" :overlay-style="{ width: '360px', maxWidth: 'calc(100vw - 24px)' }">
    <template #content>
      <a-spin :spinning="loading">
        <div class="quality-config">
          <div>
            <div class="quality-config-title">模型质量检测配置</div>
            <div class="quality-config-help">快速与深度互斥；默认使用快速检测。</div>
          </div>
          <div class="quality-config-row">
            <div>
              <div class="quality-config-label">深度检测</div>
              <div class="quality-config-help">开启后手工、定时和恢复检查统一使用深度模式。</div>
            </div>
            <a-switch v-model:checked="form.deepDetection" :disabled="saving" />
          </div>
          <div class="quality-config-row">
            <div>
              <div class="quality-config-label">手动测试处罚</div>
              <div class="quality-config-help">关闭时仍记录日志并同步健康监控，只不修改账户。</div>
            </div>
            <a-switch v-model:checked="form.manualEnforcementEnabled" :disabled="saving" />
          </div>
          <label class="quality-config-field">
            <span>处罚阈值</span>
            <a-input-number v-model:value="form.penaltyThreshold" :min="40" :max="100" :precision="0" :disabled="saving" addon-after="分" />
          </label>
          <label class="quality-config-field">
            <span>处罚方式</span>
            <a-select v-model:value="form.penaltyAction" :disabled="saving" :options="penaltyOptions" />
          </label>
          <label class="quality-config-field">
            <span>质量隔离恢复周期</span>
            <a-input-number v-model:value="form.recoveryIntervalMinutes" :min="10" :max="10080" :precision="0" :disabled="saving" addon-after="分钟" />
          </label>
          <div class="quality-config-actions">
            <a-button :disabled="saving" @click="open = false">取消</a-button>
            <a-button type="primary" :loading="saving" @click="save">保存</a-button>
          </div>
        </div>
      </a-spin>
    </template>
    <a-tooltip title="模型质量检测配置">
      <a-button aria-label="模型质量检测配置" :disabled="disabled" @click="emit('open')">
        <template #icon><SettingOutlined /></template>
      </a-button>
    </a-tooltip>
  </a-popover>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { SettingOutlined } from '@ant-design/icons-vue'
import type { ModelQualityPenaltyAction, ModelQualityPolicy, ModelQualityPolicyUpdateInput } from '@/types/domain'

const props = defineProps<{
  disabled?: boolean
  loading: boolean
  policy: ModelQualityPolicy
  saving: boolean
}>()
const emit = defineEmits<{
  (event: 'open'): void
  (event: 'save', value: ModelQualityPolicyUpdateInput): void
}>()
const open = ref(false)
const form = reactive({
  deepDetection: false,
  manualEnforcementEnabled: true,
  penaltyThreshold: 70,
  penaltyAction: 'fallback' as ModelQualityPenaltyAction,
  recoveryIntervalMinutes: 10
})
const penaltyOptions = [
  { label: '降级备用', value: 'fallback' },
  { label: '停用', value: 'disable' },
  { label: '质量隔离', value: 'quality_isolate' }
]

watch(() => props.policy, (policy) => {
  form.deepDetection = policy.profile === 'full'
  form.manualEnforcementEnabled = policy.manualEnforcementEnabled
  form.penaltyThreshold = policy.penaltyThreshold
  form.penaltyAction = policy.penaltyAction
  form.recoveryIntervalMinutes = policy.recoveryIntervalMinutes
}, { immediate: true, deep: true })

function save() {
  emit('save', {
    expectedRevision: props.policy.revision,
    profile: form.deepDetection ? 'full' : 'quick',
    manualEnforcementEnabled: form.manualEnforcementEnabled,
    penaltyThreshold: Math.trunc(form.penaltyThreshold),
    penaltyAction: form.penaltyAction,
    recoveryIntervalMinutes: Math.trunc(form.recoveryIntervalMinutes)
  })
}
</script>

<style scoped>
.quality-config { display: grid; gap: 14px; }
.quality-config-title, .quality-config-label { color: #0f172a; font-weight: 600; }
.quality-config-help { margin-top: 3px; color: #64748b; font-size: 12px; line-height: 1.5; }
.quality-config-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.quality-config-field { display: grid; gap: 6px; color: #334155; font-size: 13px; font-weight: 600; }
.quality-config-field :deep(.ant-select), .quality-config-field :deep(.ant-input-number-group-wrapper) { width: 100%; }
.quality-config-actions { display: flex; justify-content: flex-end; gap: 8px; }
</style>
