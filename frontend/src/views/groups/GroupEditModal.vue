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
      <a-form-item label="分组形态" required tooltip="普通分组直接拥有账号；超级分组只组合同一所有者的普通分组，作为一个分组目标被路由绑定。分组形态创建后不可修改；成员可随时添加，未配置启用成员时该目标暂不可用。">
        <a-radio-group
          v-model:value="form.groupKind"
          button-style="solid"
          :disabled="groupKindLocked"
        >
          <a-radio-button value="standard">普通分组</a-radio-button>
          <a-radio-button value="super">超级分组</a-radio-button>
        </a-radio-group>
      </a-form-item>
      <div v-if="form.groupKind !== 'super'" class="scheduling-policy-grid">
        <a-form-item label="最大单账户排队阈值" tooltip="-1 表示不启用；设置后达到阈值的账户在新请求排序中让位，让账户使用更平均。">
          <a-input-number
            :value="form.schedulingPolicy.defaultSoftConcurrency"
            :min="-1"
            :max="1000000"
            :disabled="editingAuthorizedGroup"
            @update:value="emit('default-soft-concurrency-change', $event)"
          />
        </a-form-item>
        <a-form-item label="单 IP 并发限制" tooltip="0 表示不限制；设置后同一 IP 超过并发立即返回 429。">
          <a-input-number
            :value="form.schedulingPolicy.clientIpConcurrencyLimit"
            :min="0"
            :max="1000000"
            :disabled="editingAuthorizedGroup"
            @update:value="emit('client-ip-concurrency-limit-change', $event)"
          />
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
import type { GroupEditForm } from './groupFormModel'

defineProps<{
  editingAuthorizedGroup: boolean
  form: GroupEditForm
  groupKindLocked: boolean
  open: boolean
  saving: boolean
  showTargetAlert: boolean
  targetSystemAccountLabel?: string
  title: string
}>()

const emit = defineEmits<{
  (event: 'client-ip-concurrency-limit-change', value: unknown): void
  (event: 'default-soft-concurrency-change', value: unknown): void
  (event: 'save'): void
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

@media (max-width: 640px) {
  .scheduling-policy-grid {
    grid-template-columns: 1fr;
  }
}
</style>
