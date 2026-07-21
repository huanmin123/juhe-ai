<template>
  <a-modal v-model:open="open" title="新增授权" width="680px" :confirm-loading="saving" :ok-button-props="{ disabled: saving }" @ok="$emit('ok')">
    <a-form layout="vertical">
      <a-form-item v-if="isManagementView" label="授权人" required tooltip="管理端需要先指定授权人，再从该用户自己的资源中选择授权资源；管理员可选择自己，等同于代自己授权。">
        <SystemPrincipalSelect
          v-model:value="form.ownerSystemAccountId"
          :accounts="ownerUsers"
          :active-only="false"
          :filter-option="false"
          :loading="ownerUsersLoading"
          placeholder="选择资源归属用户"
          @dropdown-visible-change="$emit('owner-dropdown', $event)"
          @search="$emit('owner-search', $event)"
          @change="$emit('owner-change')"
        />
      </a-form-item>
      <a-form-item class="inline-radio-form-item">
        <div class="inline-radio-row">
          <span class="inline-radio-label">
            <span class="inline-radio-required" aria-hidden="true">*</span>
            授权资源类型
          </span>
          <a-radio-group v-model:value="form.resourceType" aria-label="授权资源类型" class="compact-radio-group" size="small">
            <a-radio-button v-for="option in resourceTypeOptions" :key="option.value" :value="option.value">
              {{ option.label }}
            </a-radio-button>
          </a-radio-group>
        </div>
      </a-form-item>
      <a-form-item label="授权资源" required>
        <GroupSelect
          v-if="form.resourceType === 'group'"
          v-model:value="form.resourceId"
          v-model:selected-group="form.resourceGroup"
          :filter-option="false"
          :loading="resourceLoading"
          :options="resourceOptions"
          :disabled="resourceSelectDisabled"
          :placeholder="resourcePlaceholder"
          @dropdown-visible-change="$emit('resource-dropdown', $event)"
          @search="$emit('resource-search', $event)"
        />
        <AccountSelect
          v-else
          v-model:value="form.resourceId"
          v-model:selected-account="form.resourceAccount"
          cache-key="accounts"
          :filter-option="false"
          :loading="resourceLoading"
          :options="resourceOptions"
          :disabled="resourceSelectDisabled"
          :placeholder="resourcePlaceholder"
          @dropdown-visible-change="$emit('resource-dropdown', $event)"
          @search="$emit('resource-search', $event)"
        />
      </a-form-item>
      <a-form-item class="inline-radio-form-item">
        <div class="inline-radio-row">
          <span class="inline-radio-label">
            <span class="inline-radio-required" aria-hidden="true">*</span>
            授权对象类型
          </span>
          <a-radio-group v-model:value="form.granteeType" aria-label="授权对象类型" class="compact-radio-group" size="small">
            <a-radio-button value="system_account">个人</a-radio-button>
            <a-radio-button value="team">团队</a-radio-button>
          </a-radio-group>
        </div>
      </a-form-item>
      <a-form-item
        :label="form.granteeType === 'system_account' ? '被授权用户' : '被授权团队'"
        required
        :tooltip="form.granteeType === 'team' ? '团队授权会自动展开到团队内所有启用成员；成员移除后，对应团队来源授权也会自动回收。' : undefined"
      >
        <SystemPrincipalSelect
          v-model:value="form.granteeId"
          :accounts="users"
          :teams="teams"
          :excluded-ids="form.granteeType === 'system_account' ? excludedGranteeIds : []"
          :scope="form.granteeType === 'system_account' ? 'system_account' : 'team'"
          :filter-option="false"
          :loading="granteeLoading"
          :disabled="isManagementView && !form.ownerSystemAccountId"
          :not-found-content="granteeNotFoundContent"
          :placeholder="isManagementView && !form.ownerSystemAccountId ? '请先选择授权人' : form.granteeType === 'system_account' ? '选择一个用户' : '选择一个被授权团队'"
          @dropdown-visible-change="$emit('grantee-dropdown', $event)"
          @search="$emit('grantee-search', $event)"
        />
      </a-form-item>
      <a-form-item v-if="targetGroupVisible" label="目标分组" required :tooltip="targetGroupTip">
        <GroupSelect
          v-model:value="form.targetGroupId"
          v-model:selected-group="form.targetGroup"
          :disabled="targetGroupDisabled"
          :filter-option="false"
          :groups="targetGroups"
          :loading="targetGroupLoading"
          :placeholder="targetGroupPlaceholder"
          @dropdown-visible-change="$emit('target-group-dropdown', $event)"
          @search="$emit('target-group-search', $event)"
        />
      </a-form-item>
      <a-form-item label="说明">
        <a-textarea v-model:value="form.remark" :rows="3" placeholder="可选，填写授权用途或范围说明" />
      </a-form-item>
      <a-form-item label="到期时间" tooltip="可选，支持选择明天 0 点或中午 12 点，到期后授权自动变为“授权到期”。">
        <a-date-picker v-model:value="form.expiresAt" show-time allow-clear :disabled-date="disabledDate" style="width: 100%" />
      </a-form-item>
      <RequestQuotaFields :model="form.quotaLimits" />
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { Dayjs } from 'dayjs'
import AccountSelect from '@/components/AccountSelect.vue'
import GroupSelect from '@/components/GroupSelect.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { AuthorizationGranteeGroupOptionSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import RequestQuotaFields from '../shared/RequestQuotaFields.vue'
import type { AuthorizationCreateFormModel } from './authorizationFormTypes'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  form: AuthorizationCreateFormModel
  excludedGranteeIds: string[]
  hasGranteeOptions: boolean
  isManagementView: boolean
  ownerUsers: SystemAccountPrincipalSummary[]
  ownerUsersLoading?: boolean
  resourceLoading?: boolean
  resourceOptions: Array<{ label: string; value: string }>
  resourcePlaceholder: string
  resourceSelectDisabled: boolean
  resourceTypeOptions: Array<{ label: string; value: 'account' | 'group' }>
  saving?: boolean
  disabledDate?: (date: Dayjs) => boolean
  targetGroupLoading?: boolean
  targetGroupDisabled: boolean
  targetGroupPlaceholder: string
  targetGroupTip: string
  targetGroupVisible: boolean
  targetGroups: AuthorizationGranteeGroupOptionSummary[]
  teams: SystemTeamPrincipalSummary[]
  granteeLoading?: boolean
  users: SystemAccountPrincipalSummary[]
}>()

const granteeNotFoundContent = computed(() => props.hasGranteeOptions
  ? undefined
  : props.form.granteeType === 'system_account'
    ? '暂无可授权用户'
    : '暂无可被授权团队')

defineEmits<{
  (event: 'grantee-dropdown', open: boolean): void
  (event: 'grantee-search', value: string): void
  (event: 'ok'): void
  (event: 'owner-change'): void
  (event: 'owner-dropdown', open: boolean): void
  (event: 'owner-search', value: string): void
  (event: 'resource-dropdown', open: boolean): void
  (event: 'resource-search', value: string): void
  (event: 'target-group-dropdown', open: boolean): void
  (event: 'target-group-search', value: string): void
}>()
</script>

<style scoped>
.inline-radio-form-item {
  margin-bottom: 16px;
}

.inline-radio-row {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}

.inline-radio-label {
  color: var(--ant-color-text, rgba(0, 0, 0, 0.88));
  line-height: 24px;
}

.inline-radio-required {
  color: var(--ant-color-error, #ff4d4f);
  margin-inline-end: 4px;
}

.compact-radio-group :deep(.ant-radio-button-wrapper) {
  font-size: 13px;
  height: 26px;
  line-height: 24px;
  padding-inline: 12px;
}

@media (max-width: 575px) {
  .inline-radio-row {
    align-items: flex-start;
    flex-direction: column;
    gap: 6px;
  }
}
</style>
