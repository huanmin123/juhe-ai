<template>
  <!-- bench 变体：编辑工作台"常用配置区"：加入分组 + 标签/说明；名称/调度/状态由弹框标题栏与摘要卡承载 -->
  <section v-if="variant === 'bench'" class="form-section">
    <AccountGroupField
      :form="form"
      :group-options="groupOptions"
      :group-options-loading="groupOptionsLoading"
      @group-options-dropdown="$emit('group-options-dropdown', $event)"
      @group-options-search="$emit('group-options-search', $event)"
    />
    <AccountMetaFields
      v-if="!authorizedEditing"
      :deleting-tag-id="deletingTagId"
      :form="form"
      :tag-options="tagOptions"
      :tag-options-loading="tagOptionsLoading"
      @delete-tag="$emit('delete-tag', $event)"
      @tag-options-dropdown="$emit('tag-options-dropdown', $event)"
    />
  </section>
  <!-- full 变体：仅创建向导第 2 步；「加入分组」已上移第 1 步（AccountEditModal 内 AccountGroupField），此处只保留标签/说明与调度区 -->
  <section v-else class="form-section">
    <div class="form-grid attribution-grid">
      <AccountMetaFields
        :deleting-tag-id="deletingTagId"
        :form="form"
        :readonly="authorizedEditing"
        :tag-options="tagOptions"
        :tag-options-loading="tagOptionsLoading"
        @delete-tag="$emit('delete-tag', $event)"
        @tag-options-dropdown="$emit('tag-options-dropdown', $event)"
      />
    </div>
    <div class="dispatch-config-grid">
      <a-form-item label="并发上限" tooltip="这个账号同一时间最多承接多少个请求。达到上限后，调度会等待或尝试其他可用账号。">
        <a-input-number v-model:value="form.concurrencyLimit" :disabled="authorizedEditing" :min="1" style="width: 100%" />
      </a-form-item>
      <a-form-item label="优先级" tooltip="分组内账号排序使用小值优先；0 会排在 1 前面。授权账号这里表示当前使用方本地分组内的调度优先级。">
        <a-input-number v-model:value="form.priority" :min="0" style="width: 100%" />
      </a-form-item>
      <a-form-item label="特权" tooltip="同族改名映射与直连同层参与调度排序；跨协议转换映射排在直连之后。超级优先只在其模型命中层级内提权。">
        <a-select v-model:value="form.privilege" :disabled="authorizedEditing" style="width: 100%">
          <a-select-option value="normal">无</a-select-option>
          <a-select-option value="super_priority">超级优先</a-select-option>
          <a-select-option value="fallback">降级备用</a-select-option>
        </a-select>
      </a-form-item>
      <a-form-item class="dispatch-status-field" label="状态">
        <a-radio-group v-model:value="form.status" :disabled="authorizedEditing" @change="form.statusSelectionExplicit = true">
          <a-radio value="active">可调度</a-radio>
          <a-radio value="pending_test">待检查</a-radio>
          <a-radio value="disabled">停用</a-radio>
        </a-radio-group>
      </a-form-item>
    </div>
  </section>
</template>

<script setup lang="ts">
import type { AccountTagSummary } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import AccountGroupField from './AccountGroupField.vue'
import AccountMetaFields from './AccountMetaFields.vue'

withDefaults(defineProps<{
  authorizedEditing: boolean
  editing: boolean
  form: AccountFormModel
  groupOptionsLoading: boolean
  groupOptions: Array<{ label: string; value: string }>
  tagOptionsLoading: boolean
  tagOptions: AccountTagSummary[]
  deletingTagId?: string
  variant?: 'full' | 'bench'
}>(), {
  variant: 'full'
})

defineEmits<{
  (event: 'delete-tag', tagId: string): void
  (event: 'group-options-dropdown', open: boolean): void
  (event: 'group-options-search', value: string): void
  (event: 'tag-options-dropdown', open: boolean): void
}>()
</script>

<style scoped>
.form-section {
  padding: 2px 0 4px;
  border-bottom: 1px solid var(--juhe-border);
  background: transparent;
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 18px;
}

.attribution-grid :deep(.tag-form-item) {
  grid-column: auto;
}

.dispatch-config-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(96px, 118px)) minmax(220px, 1fr);
  gap: 0 18px;
}

.dispatch-config-grid > * {
  min-width: 0;
}

.dispatch-status-field :deep(.ant-radio-group) {
  display: inline-flex;
  flex-wrap: nowrap;
  gap: 12px;
}

.dispatch-status-field :deep(.ant-radio-wrapper) {
  margin-inline-end: 0;
  white-space: nowrap;
}

.form-help {
  margin-top: 4px;
  color: var(--juhe-muted);
  font-size: 12px;
}

@media (max-width: 992px) {
  .form-grid {
    grid-template-columns: 1fr;
  }

  .dispatch-config-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 640px) {
  .dispatch-config-grid {
    grid-template-columns: 1fr;
  }
}
</style>
