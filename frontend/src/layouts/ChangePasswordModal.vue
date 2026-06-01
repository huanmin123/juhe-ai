<template>
  <a-modal
    v-model:open="open"
    :cancel-button-props="cancelButtonProps"
    :closable="!forced"
    :confirm-loading="saving"
    :keyboard="!forced"
    :mask-closable="!forced"
    :ok-text="forced ? '保存并进入控制台' : '确定'"
    :title="forced ? '修改初始密码' : '修改登录密码'"
    @ok="$emit('ok')"
  >
    <a-form layout="vertical">
      <a-form-item v-if="requireOldPassword" label="当前密码">
        <a-input-password v-model:value="form.oldPassword" autocomplete="current-password" placeholder="请输入当前密码" />
      </a-form-item>
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
import { computed } from 'vue'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  forced?: boolean
  form: {
    oldPassword?: string
    newPassword: string
    confirmPassword: string
  }
  requireOldPassword?: boolean
  saving: boolean
}>()

const cancelButtonProps = computed(() => ({
  disabled: Boolean(props.forced || props.saving)
}))

defineEmits<{
  (event: 'ok'): void
}>()
</script>
