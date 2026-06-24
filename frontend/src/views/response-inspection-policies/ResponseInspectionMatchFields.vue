<template>
  <div class="response-match-fields">
    <a-form-item label="请求客户端" tooltip="只匹配指定客户端画像的请求；不选表示不限制客户端来源。">
      <a-select
        v-model:value="form.clientProfiles"
        :disabled="disabled"
        :options="responseInspectionClientProfileOptions"
        mode="multiple"
        placeholder="不限请求客户端"
        allow-clear
      />
    </a-form-item>
    <a-form-item label="账号兼容能力" tooltip="按账号声明的客户端兼容能力过滤规则适用范围；不选表示不限制。">
      <a-select
        v-model:value="form.accountClientCompatibilities"
        :disabled="disabled"
        :options="responseInspectionAccountCompatibilityOptions"
        mode="multiple"
        placeholder="不限兼容能力"
        allow-clear
      />
    </a-form-item>
    <a-form-item label="输出文本包含" tooltip="匹配模型正常输出文本中的关键词，适合识别广告、污染内容或固定异常提示。">
      <a-textarea v-model:value="form.outputTextIncludes" :disabled="disabled" :rows="1" auto-size placeholder="广告关键词, 异常提示" />
    </a-form-item>
    <a-form-item label="输出文本排除" tooltip="命中这些关键词时不触发规则，用来减少正常回复被误判。">
      <a-textarea v-model:value="form.outputTextExcludes" :disabled="disabled" :rows="1" auto-size placeholder="正常提示, 合法结果" />
    </a-form-item>
    <a-form-item label="error.code" tooltip="匹配响应体里的 error.code 字段。常用于供应商在 200 响应里包了一层业务错误的情况。">
      <a-input v-model:value="form.errorCodes" :disabled="disabled" placeholder="cyber_policy" />
    </a-form-item>
    <a-form-item label="error.type" tooltip="匹配响应体里的 error.type 字段。不同供应商字段可能不同，可从审计日志确认。">
      <a-input v-model:value="form.errorTypes" :disabled="disabled" placeholder="server_error" />
    </a-form-item>
    <a-form-item label="错误消息包含" tooltip="匹配错误消息文本里的关键词，适合识别私有错误提示或策略拦截文案。">
      <a-textarea v-model:value="form.errorMessageIncludes" :disabled="disabled" :rows="1" auto-size placeholder="upstream policy blocked, rate limit" />
    </a-form-item>
    <a-form-item label="完成原因 / 状态" tooltip="匹配 finish_reason、status 等完成状态字段，例如 failed、content_filter、length。">
      <a-input v-model:value="form.finishReasons" :disabled="disabled" placeholder="failed, content_filter, length" />
    </a-form-item>
    <a-form-item label="JSON字段路径存在" tooltip="匹配响应 JSON 中是否存在指定路径，例如 response.error 或 choices.0.message.content。">
      <a-input v-model:value="form.jsonPathsExists" :disabled="disabled" placeholder="response.error, choices.0.message.content" />
    </a-form-item>
    <a-form-item label="SSE 事件原文包含" tooltip="匹配流式 SSE 原文片段，适合识别 response.failed 这类事件或非标准流式错误。">
      <a-textarea v-model:value="form.rawTextIncludes" :disabled="disabled" :rows="1" auto-size placeholder="event: response.failed" />
    </a-form-item>
  </div>
</template>

<script setup lang="ts">
import {
  responseInspectionAccountCompatibilityOptions,
  responseInspectionClientProfileOptions,
  type ResponseInspectionMatchFormFields
} from './responseInspectionPolicyForm'

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
