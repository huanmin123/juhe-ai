<template>
  <a-modal
    v-model:open="open"
    :title="modalTitle"
    width="980px"
    :confirm-loading="saving"
    :ok-button-props="{ type: 'primary', disabled: saving || readOnly }"
    :footer="readOnly ? null : undefined"
    @ok="submitForm"
    @cancel="$emit('cancel')"
  >
    <a-form layout="vertical" class="policy-form">
      <section class="form-section">
        <div class="form-section-title">基础</div>
        <div class="form-grid three">
          <a-form-item label="策略名称" required>
            <a-input v-model:value="form.name" :disabled="readOnly" placeholder="例如 中转广告污染拦截" />
          </a-form-item>
          <a-form-item label="协议" required>
            <a-select
              v-model:value="form.protocolCode"
              :disabled="readOnly"
              :options="protocolOptions"
              placeholder="选择协议"
              @change="handleProtocolChange"
            />
          </a-form-item>
          <a-form-item label="作用层级" required>
            <a-segmented v-model:value="form.scopeType" :disabled="readOnly" :options="scopeOptions" block @change="handleScopeChange" />
          </a-form-item>
          <a-form-item v-if="form.scopeType === 'provider'" label="供应商" required>
            <a-select
              v-model:value="form.providerCode"
              :disabled="readOnly"
              :options="protocolProviderOptions"
              placeholder="选择同协议供应商"
              show-search
              option-filter-prop="label"
            />
          </a-form-item>
          <a-form-item label="优先级">
            <a-input-number v-model:value="form.priority" :disabled="readOnly" :min="1" :max="9999" style="width: 100%" />
          </a-form-item>
        </div>
        <a-form-item label="启用状态">
          <a-switch v-model:checked="form.enabled" :disabled="readOnly" checked-children="启用" un-checked-children="停用" />
        </a-form-item>
      </section>

      <section class="form-section">
        <div class="form-section-title">匹配条件</div>
        <ResponseInspectionMatchFields :form="form" :disabled="readOnly" />
      </section>

      <section class="form-section">
        <div class="form-section-title">处置</div>
        <a-form-item label="处置模板">
          <ResponseInspectionActionSelector v-model="form.action" :disabled="readOnly" />
        </a-form-item>
        <a-form-item label="备注">
          <a-textarea v-model:value="form.notes" :disabled="readOnly" :rows="2" placeholder="可写污染来源或排障线索" />
        </a-form-item>
      </section>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, reactive, watch } from 'vue'

import type { ResponseInspectionPolicyPayload } from '@/api/client'
import type {
  ResponseInspectionPolicyAction,
  ResponseInspectionPolicyScopeType,
  ResponseInspectionPolicySummary
} from '@/types/domain'
import ResponseInspectionActionSelector from './ResponseInspectionActionSelector.vue'
import ResponseInspectionMatchFields from './ResponseInspectionMatchFields.vue'
import {
  buildResponseInspectionMatchPayload,
  formatResponseInspectionList,
  hasPositiveResponseInspectionMatcher,
  normalizeResponseInspectionAccountCompatibilities,
  normalizeResponseInspectionClientProfiles,
  type ResponseInspectionMatchFormFields,
  validateResponseInspectionMatchFields
} from './responseInspectionPolicyForm'

type ResponseInspectionPolicyFormMode = 'create' | 'edit' | 'view'

interface ResponseInspectionPolicyForm extends ResponseInspectionMatchFormFields {
  name: string
  enabled: boolean
  priority: number
  scopeType: ResponseInspectionPolicyScopeType
  protocolCode: string
  providerCode: string
  clientProfiles: ResponseInspectionMatchFormFields['clientProfiles']
  accountClientCompatibilities: ResponseInspectionMatchFormFields['accountClientCompatibilities']
  outputTextIncludes: string
  finishReasons: string
  errorCodes: string
  errorTypes: string
  errorMessageIncludes: string
  rawTextIncludes: string
  outputTextExcludes: string
  jsonPathsExists: string
  action: ResponseInspectionPolicyAction
  notes: string
}

const open = defineModel<boolean>('open', { required: true })

const props = withDefaults(defineProps<{
  mode: ResponseInspectionPolicyFormMode
  policy?: ResponseInspectionPolicySummary
  saving?: boolean
  providerOptions: Array<{ label: string; value: string; protocolCode: string }>
  defaultPriority: number
  defaultProviderCode: string
}>(), {
  saving: false
})

const emit = defineEmits<{
  submit: [payload: ResponseInspectionPolicyPayload]
  cancel: []
}>()

const scopeOptions = [
  { label: '供应商层', value: 'provider' },
  { label: '协议层', value: 'protocol' }
]
const protocolOptions = [
  { label: 'OpenAI v1', value: 'openai' },
  { label: 'Anthropic v1', value: 'anthropic' }
]
const form = reactive<ResponseInspectionPolicyForm>(defaultForm())
const readOnly = computed(() => props.mode === 'view')
const protocolProviderOptions = computed(() => props.providerOptions.filter((option) => option.protocolCode === form.protocolCode))
const modalTitle = computed(() => {
  if (props.mode === 'view') return '查看默认策略'
  return props.mode === 'edit' ? '编辑响应检查策略' : '新建响应检查策略'
})

watch(open, (isOpen) => {
  if (isOpen) resetFormForMode()
})

function resetFormForMode(): void {
  if (props.mode === 'create') {
    Object.assign(form, defaultForm(), {
      priority: props.defaultPriority,
      providerCode: props.defaultProviderCode
    })
    return
  }
  if (props.policy) {
    fillForm(props.policy)
  }
}

function defaultForm(): ResponseInspectionPolicyForm {
  return {
    name: '',
    enabled: true,
    priority: 1,
    scopeType: 'provider',
    protocolCode: 'openai',
    providerCode: '',
    clientProfiles: [],
    accountClientCompatibilities: [],
    outputTextIncludes: '',
    finishReasons: '',
    errorCodes: '',
    errorTypes: '',
    errorMessageIncludes: '',
    rawTextIncludes: '',
    outputTextExcludes: '',
    jsonPathsExists: '',
    action: 'retry_no_avoidance',
    notes: ''
  }
}

function fillForm(policy: ResponseInspectionPolicySummary): void {
  Object.assign(form, {
    name: policy.name,
    enabled: policy.enabled,
    priority: policy.priority,
    scopeType: policy.scopeType,
    protocolCode: policy.protocolCode,
    providerCode: policy.providerCode ?? '',
    clientProfiles: normalizeResponseInspectionClientProfiles(policy.match.clientProfiles),
    accountClientCompatibilities: normalizeResponseInspectionAccountCompatibilities(policy.match.accountClientCompatibilities),
    outputTextIncludes: formatResponseInspectionList(policy.match.outputTextIncludes),
    finishReasons: formatResponseInspectionList(policy.match.finishReasons),
    errorCodes: formatResponseInspectionList(policy.match.errorCodes),
    errorTypes: formatResponseInspectionList(policy.match.errorTypes),
    errorMessageIncludes: formatResponseInspectionList(policy.match.errorMessageIncludes),
    rawTextIncludes: formatResponseInspectionList(policy.match.rawTextIncludes),
    outputTextExcludes: formatResponseInspectionList(policy.match.outputTextExcludes),
    jsonPathsExists: formatResponseInspectionList(policy.match.jsonPathsExists),
    action: policy.action,
    notes: policy.notes ?? ''
  })
}

function submitForm(): void {
  if (readOnly.value) return
  const validationMessage = validateForm()
  if (validationMessage) {
    message.warning(validationMessage)
    return
  }
  emit('submit', buildPayload())
}

function buildPayload(): ResponseInspectionPolicyPayload {
  return {
    name: form.name.trim(),
    enabled: form.enabled,
    priority: requiredPositiveInt(form.priority, '优先级', 9999),
    scopeType: form.scopeType,
    protocolCode: form.protocolCode,
    providerCode: form.scopeType === 'provider' ? form.providerCode.trim() : undefined,
    match: buildResponseInspectionMatchPayload(form),
    action: form.action,
    notes: form.notes.trim() || undefined
  }
}

function validateForm(): string | undefined {
  if (!form.name.trim()) return '请填写策略名称'
  if (!protocolOptions.some((option) => option.value === form.protocolCode)) return '请选择协议'
  if (form.scopeType === 'provider' && !form.providerCode.trim()) return '请选择供应商'
  if (!positiveInt(form.priority, 9999)) return '优先级必须是 1-9999 的整数'
  const listValidation = validateResponseInspectionMatchFields(form)
  if (listValidation) return listValidation
  if (!hasPositiveResponseInspectionMatcher(form)) return '至少需要填写一个匹配条件'
  return undefined
}

function handleScopeChange(): void {
  if (readOnly.value) return
  if (form.scopeType === 'provider' && !form.providerCode) {
    form.providerCode = defaultProviderCodeForProtocol()
  }
  if (form.scopeType === 'protocol') {
    form.providerCode = ''
  }
}

function handleProtocolChange(): void {
  if (readOnly.value) return
  if (form.scopeType === 'provider') {
    form.providerCode = defaultProviderCodeForProtocol()
  }
}

function defaultProviderCodeForProtocol(): string {
  const preferred = protocolProviderOptions.value.find((option) => option.value === props.defaultProviderCode)
  return preferred?.value ?? protocolProviderOptions.value[0]?.value ?? ''
}

function positiveInt(value: unknown, max = Number.POSITIVE_INFINITY): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value) && value > 0 && value <= max ? value : undefined
}

function requiredPositiveInt(value: unknown, label: string, max = Number.POSITIVE_INFINITY): number {
  const numberValue = positiveInt(value, max)
  if (!numberValue) throw new Error(`${label}无效`)
  return numberValue
}
</script>

<style scoped>
.policy-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.form-section {
  padding: 14px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: #fff;
}

.form-section-title {
  margin-bottom: 12px;
  color: #111827;
  font-size: 15px;
  font-weight: 700;
}

.form-grid {
  display: grid;
  gap: 0 14px;
}

.form-grid.three {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

@media (max-width: 820px) {
  .form-grid.three {
    grid-template-columns: 1fr;
  }
}
</style>
