<template>
  <a-card :title="title" class="page-card chart-card" :loading="loading">
    <p v-if="description" class="chart-card-description">{{ description }}</p>
    <a-alert v-if="error && hasData" type="error" show-icon :message="error" class="chart-card-error">
      <template #action>
        <a-button type="link" size="small" @click="onRetry?.()">重试</a-button>
      </template>
    </a-alert>
    <a-empty v-if="!hasData" :description="error || emptyDescription">
      <a-button v-if="error" type="link" @click="onRetry?.()">重试</a-button>
    </a-empty>
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
}>()
</script>

<style scoped>
.chart-card {
  width: 100%;
  height: 100%;
}

.chart-card-description {
  margin: -4px 0 12px;
  color: #64748b;
  font-size: 12px;
  line-height: 1.6;
}

.chart-card-error {
  margin-bottom: 12px;
}

.chart-card :deep(.ant-card-body) {
  display: flex;
  flex: 1;
  flex-direction: column;
  min-height: 328px;
}

.chart-card :deep(.ant-empty) {
  display: flex;
  flex: 1;
  flex-direction: column;
  justify-content: center;
}
</style>
