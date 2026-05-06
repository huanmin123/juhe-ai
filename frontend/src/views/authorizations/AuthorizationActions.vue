<template>
  <div :class="compact ? 'authorization-actions' : 'mobile-list-card-actions two-actions'">
    <a-button :size="compact ? 'small' : undefined" @click="$emit('usage-detail')">明细</a-button>
    <a-dropdown>
      <a-button :size="compact ? 'small' : undefined">
        更多
      </a-button>
      <template #overlay>
        <a-menu @click="$emit('menu-click', $event)">
          <a-menu-item key="edit-expire">修改配置</a-menu-item>
          <a-menu-item v-if="authorization.status === 'active'" key="pause">暂停授权</a-menu-item>
          <a-menu-item v-if="authorization.status === 'paused'" key="resume">恢复授权</a-menu-item>
          <a-menu-item v-if="authorization.status === 'active' && hasManualSource(authorization)" key="revoke-manual">回收</a-menu-item>
          <a-sub-menu v-if="activeTeamSources(authorization).length" key="revoke-team" title="回收">
            <a-menu-item v-for="teamSource in activeTeamSources(authorization)" :key="`team:${teamSource.sourceTeamId}`">
              {{ teamSource.sourceTeamName || teamSource.sourceTeamId }}
            </a-menu-item>
          </a-sub-menu>
        </a-menu>
      </template>
    </a-dropdown>
  </div>
</template>

<script setup lang="ts">
import type { ResourceAuthorizationSummary } from '@/types/domain'
import { activeTeamSources, hasManualSource } from './authorizationFormatters'

defineProps<{
  authorization: ResourceAuthorizationSummary
  compact?: boolean
}>()

defineEmits<{
  (event: 'menu-click', menuEvent: { key: string | number }): void
  (event: 'usage-detail'): void
}>()
</script>

<style scoped>
.authorization-actions {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
</style>
