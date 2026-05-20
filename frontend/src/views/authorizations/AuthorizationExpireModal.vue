<template>
  <a-modal v-model:open="open" title="修改授权配置" width="640px" @ok="$emit('ok')">
    <a-form layout="vertical">
      <a-form-item label="到期时间">
        <a-date-picker v-model:value="form.expiresAt" show-time allow-clear :disabled-date="disabledDate" style="width: 100%" />
        <div class="form-help">清空后表示不设置自动回收时间。</div>
      </a-form-item>
      <RequestQuotaFields :model="form.quotaLimits" />
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import type { Dayjs } from 'dayjs'
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
</script>
