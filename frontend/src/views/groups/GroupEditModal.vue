<template>
  <a-modal
    :open="open"
    :title="title"
    width="640px"
    :confirm-loading="saving"
    :ok-button-props="{ type: 'primary', disabled: saving }"
    @ok="emit('save')"
    @update:open="emit('update:open', $event)"
  >
    <a-alert v-if="showTargetAlert && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
    <a-form layout="vertical" class="modal-form">
      <a-form-item label="分组名称" required>
        <a-input v-model:value="form.name" :disabled="editingAuthorizedGroup" />
      </a-form-item>
      <a-form-item label="所属供应商" required tooltip="供应商决定这个分组后续可绑定的账户范围。">
        <a-select
          v-model:value="form.providerCode"
          :options="providerOptions"
          :loading="providerOptionsLoading"
          :disabled="providerLocked || editingAuthorizedGroup"
          @dropdown-visible-change="emit('provider-dropdown-visible-change', $event)"
        />
      </a-form-item>
      <a-form-item label="分组类型" required tooltip="个人分组按账号并发直接调度；高并发分组会启用短队列、单账户软阈值和可选单 IP 并发限制。">
        <a-radio-group v-model:value="form.groupType" button-style="solid">
          <a-radio-button value="personal">个人分组</a-radio-button>
          <a-radio-button value="high_concurrency">高并发分组</a-radio-button>
        </a-radio-group>
      </a-form-item>
      <div v-if="form.groupType === 'high_concurrency'" class="scheduling-policy-grid">
        <a-form-item label="最大单账户排队阈值" tooltip="达到该阈值后优先切到其他账户；实际值不会超过账户并发上限。">
          <a-input-number v-model:value="form.schedulingPolicy.defaultSoftConcurrency" :min="1" :max="1000000" />
        </a-form-item>
        <a-form-item label="最大等待时间（秒）" tooltip="所有账户硬并发都满时，请求最多在分组短队列等待这么久；超时后返回 429。">
          <a-input-number :value="maxQueueWaitSeconds" :min="1" :max="3600" @update:value="emit('max-queue-wait-seconds-change', $event)" />
        </a-form-item>
        <a-form-item class="scheduling-policy-wide" label="限制单 IP 并发" tooltip="开启后限制同一 IP 在当前分组和 API Key 下同时占用的请求数。默认关闭。">
          <a-switch :checked="clientIpLimitEnabled" checked-children="开启" un-checked-children="关闭" @update:checked="emit('update:clientIpLimitEnabled', $event)" />
        </a-form-item>
        <a-form-item label="单 IP 并发上限" tooltip="开启限制时默认 5 个并发；关闭后不限制。">
          <a-input-number :value="clientIpConcurrencyLimit" :min="1" :max="1000000" :disabled="!clientIpLimitEnabled" @update:value="emit('client-ip-concurrency-limit-change', $event)" />
        </a-form-item>
        <a-form-item label="超过限制时" tooltip="立即拒绝会返回 429；排队等待会先等同 IP 请求释放，再进入分组调度。">
          <a-segmented v-model:value="form.schedulingPolicy.clientIpConcurrencyOverflowMode" :options="clientIpOverflowModeOptions" :disabled="!clientIpLimitEnabled" block />
        </a-form-item>
      </div>
      <a-form-item label="说明" tooltip="仅用于后台识别分组用途，不参与调度规则。">
        <a-textarea v-model:value="form.description" :rows="3" :disabled="editingAuthorizedGroup" />
      </a-form-item>
      <a-form-item label="状态" tooltip="停用后该分组不会继续承接 API Key 调度请求。">
        <a-switch v-model:checked="form.enabled" checked-children="启用" un-checked-children="停用" />
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { clientIpOverflowModeOptions } from './groupSchedulingPolicy'
import type { GroupEditForm } from './groupFormModel'

defineProps<{
  clientIpConcurrencyLimit: number
  clientIpLimitEnabled: boolean
  editingAuthorizedGroup: boolean
  form: GroupEditForm
  maxQueueWaitSeconds: number
  open: boolean
  providerLocked: boolean
  providerOptions: Array<{ label: string; value: string; disabled?: boolean }>
  providerOptionsLoading: boolean
  saving: boolean
  showTargetAlert: boolean
  targetSystemAccountLabel?: string
  title: string
}>()

const emit = defineEmits<{
  (event: 'client-ip-concurrency-limit-change', value: unknown): void
  (event: 'max-queue-wait-seconds-change', value: unknown): void
  (event: 'provider-dropdown-visible-change', open: boolean): void
  (event: 'save'): void
  (event: 'update:clientIpLimitEnabled', value: boolean): void
  (event: 'update:open', value: boolean): void
}>()
</script>

<style scoped>
.modal-alert {
  margin-bottom: 16px;
}

.modal-form {
  margin-top: 16px;
}

.scheduling-policy-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: 16px;
}

.scheduling-policy-grid :deep(.ant-input-number) {
  width: 100%;
}

.scheduling-policy-wide {
  grid-column: 1 / -1;
}

@media (max-width: 640px) {
  .scheduling-policy-grid {
    grid-template-columns: 1fr;
  }
}
</style>
