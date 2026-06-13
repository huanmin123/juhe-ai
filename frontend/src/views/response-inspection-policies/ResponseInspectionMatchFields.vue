<template>
  <div class="response-match-fields">
    <a-form-item label="输出文本包含">
      <a-textarea v-model:value="form.outputTextIncludes" :disabled="disabled" :rows="1" auto-size placeholder="广告关键词, 异常提示" />
    </a-form-item>
    <a-form-item label="输出文本排除">
      <a-textarea v-model:value="form.outputTextExcludes" :disabled="disabled" :rows="1" auto-size placeholder="正常提示, 合法结果" />
    </a-form-item>
    <a-form-item label="error.code">
      <a-input v-model:value="form.errorCodes" :disabled="disabled" placeholder="cyber_policy" />
    </a-form-item>
    <a-form-item label="error.type">
      <a-input v-model:value="form.errorTypes" :disabled="disabled" placeholder="server_error" />
    </a-form-item>
    <a-form-item label="错误消息包含">
      <a-textarea v-model:value="form.errorMessageIncludes" :disabled="disabled" :rows="1" auto-size placeholder="upstream policy blocked, rate limit" />
    </a-form-item>
    <a-form-item label="完成原因 / 状态">
      <a-input v-model:value="form.finishReasons" :disabled="disabled" placeholder="failed, content_filter, length" />
    </a-form-item>
    <a-form-item label="JSON字段路径存在">
      <a-input v-model:value="form.jsonPathsExists" :disabled="disabled" placeholder="response.error, choices.0.message.content" />
    </a-form-item>
    <a-form-item label="原始事件文本包含">
      <a-textarea v-model:value="form.rawTextIncludes" :disabled="disabled" :rows="1" auto-size placeholder="event: response.failed, data: error" />
    </a-form-item>
  </div>
</template>

<script setup lang="ts">
import type { ResponseInspectionMatchFormFields } from './responseInspectionPolicyForm'

withDefaults(defineProps<{
  form: ResponseInspectionMatchFormFields
  disabled?: boolean
}>(), {
  disabled: false
})
</script>

<style scoped>
.response-match-fields {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 12px;
}

@media (max-width: 720px) {
  .response-match-fields {
    grid-template-columns: 1fr;
  }
}
</style>
