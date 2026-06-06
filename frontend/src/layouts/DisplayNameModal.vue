<template>
  <a-modal
    v-model:open="open"
    :confirm-loading="saving"
    :ok-button-props="{ disabled: saving }"
    ok-text="保存"
    title="修改名称"
    @ok="$emit('ok')"
  >
    <a-form layout="vertical">
      <a-form-item label="显示名称" required>
        <a-input
          v-model:value="form.displayName"
          :disabled="saving"
          placeholder="请输入不含空格的显示名称"
          @keyup.enter="$emit('ok')"
        />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
const open = defineModel<boolean>('open', { required: true })

defineProps<{
  form: {
    displayName: string
  }
  saving: boolean
}>()

defineEmits<{
  (event: 'ok'): void
}>()
</script>
