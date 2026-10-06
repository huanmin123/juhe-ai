<template>
  <a-modal
    v-model:open="open"
    title="修改授权配置"
    width="640px"
    :confirm-loading="expireConfirming"
    :ok-button-props="{ disabled: expireConfirming }"
    @ok="$emit('ok')"
  >
    <a-form layout="vertical">
      <a-form-item label="到期时间" tooltip="清空后表示不设置自动回收时间。">
        <a-date-picker v-model:value="form.expiresAt" show-time allow-clear :disabled-date="disabledDate" style="width: 100%" />
      </a-form-item>
      <RequestQuotaFields :model="form.quotaLimits" />
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import type { Dayjs } from 'dayjs'

import { useSubmitAction } from '@/composables/useSubmitAction'

import RequestQuotaFields from '../shared/RequestQuotaFields.vue'
import type { AuthorizationExpireFormModel } from './authorizationFormTypes'

const open = defineModel<boolean>('open', { required: true })

defineProps<{
  form: AuthorizationExpireFormModel
  disabledDate?: (date: Dayjs) => boolean
}>()

defineEmits<{
  (event: 'ok'): void
}>()

// 订阅 useAuthorizationActions.confirmExpireChange 的全局提交锁（同名 key），
// 提交期间确认按钮显示 loading 并禁用，防止重复提交。
const { submittingRef } = useSubmitAction('authorizations')
const expireConfirming = submittingRef('authorizations.updateExpire')
</script>
