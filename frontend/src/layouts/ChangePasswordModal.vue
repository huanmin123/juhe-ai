<template>
  <a-modal v-model:open="open" title="修改登录密码" :confirm-loading="saving" @ok="$emit('ok')">
    <a-form layout="vertical">
      <a-form-item label="新密码" extra="至少 4 位，保存后会解除初始密码提醒。">
        <a-input-password v-model:value="form.newPassword" autocomplete="new-password" placeholder="请输入新密码" />
      </a-form-item>
      <a-form-item label="确认密码">
        <a-input-password v-model:value="form.confirmPassword" autocomplete="new-password" placeholder="请再次输入新密码" />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
const open = defineModel<boolean>('open', { required: true })

defineProps<{
  form: {
    newPassword: string
    confirmPassword: string
  }
  saving: boolean
}>()

defineEmits<{
  (event: 'ok'): void
}>()
</script>
