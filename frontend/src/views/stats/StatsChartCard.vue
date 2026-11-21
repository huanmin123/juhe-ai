<template>
  <a-card class="page-card chart-card" :class="{ compact }" :loading="loading">
    <template #title>
      <div class="chart-card-title">
        <span>{{ title }}</span>
        <slot name="title-extra" />
      </div>
    </template>
    <p v-if="description" class="chart-card-description">{{ description }}</p>
    <a-alert v-if="error" :message="error" type="error" show-icon>
      <template v-if="onRetry" #action>
        <a-button size="small" @click="onRetry">重试</a-button>
      </template>
    </a-alert>
    <a-empty v-else-if="!hasData" :description="emptyDescription" />
    <slot v-else />
  </a-card>
</template>

<script setup lang="ts">
defineProps<{
  title: string
  description?: string
  loading: boolean
  hasData: boolean
  emptyDescription: string
  error?: string
  onRetry?: () => void
  compact?: boolean
}>()
</script>

<style scoped>
.chart-card {
  width: 100%;
  height: 100%;
}

.chart-card-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  width: 100%;
}

.chart-card-description {
  margin: -4px 0 12px;
  color: var(--juhe-muted);
  font-size: 12px;
  line-height: 1.6;
}

.chart-card :deep(.ant-card-body) {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-height: 328px;
}

.chart-card.compact :deep(.ant-card-body) {
  min-height: 220px;
}

.chart-card :deep(.ant-empty) {
  display: flex;
  flex: 1;
  flex-direction: column;
  justify-content: center;
}

@media (max-width: 768px) {
  .chart-card :deep(.ant-card-head-title) {
    overflow: visible;
    white-space: normal;
    text-overflow: clip;
  }
}
</style>
