<template>
  <a-modal v-model:open="open" title="修改到期时间" width="520px" @ok="$emit('ok')">
    <a-form layout="vertical">
      <a-form-item label="到期时间">
        <a-date-picker v-model:value="form.expiresAt" show-time allow-clear style="width: 100%" />
        <div class="form-help">清空后表示不设置自动回收时间。</div>
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import type { AuthorizationExpireFormModel } from './authorizationFormTypes'

const open = defineModel<boolean>('open', { required: true })

defineProps<{
  form: AuthorizationExpireFormModel
}>()

defineEmits<{
  (event: 'ok'): void
}>()
</script>
