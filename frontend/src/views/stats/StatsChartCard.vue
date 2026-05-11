<template>
  <a-card :title="title" class="page-card chart-card" :loading="loading">
    <p v-if="description" class="chart-card-description">{{ description }}</p>
    <a-empty v-if="!hasData" :description="emptyDescription" />
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
