<template>
  <div v-if="compact" class="metric-card-grid compact">
    <a-card v-for="item in cards" :key="item.key" class="metric-card compact-card" :loading="loading">
      <div class="metric-label">{{ item.label }}</div>
      <div class="metric-value">{{ item.value }}</div>
      <div class="metric-extra">{{ item.extra }}</div>
    </a-card>
  </div>
  <a-row v-else :gutter="[16, 16]">
    <a-col v-for="item in cards" :key="item.key" :xs="24" :sm="12" :lg="6">
      <a-card class="metric-card" :loading="loading">
        <div class="metric-label">{{ item.label }}</div>
        <div class="metric-value">{{ item.value }}</div>
        <div class="metric-extra">{{ item.extra }}</div>
      </a-card>
    </a-col>
  </a-row>
</template>

<script setup lang="ts">
export interface StatsSummaryCardItem {
  key: string
  label: string
  value: string
  extra: string
}

defineProps<{
  cards: StatsSummaryCardItem[]
  loading: boolean
  compact?: boolean
}>()
</script>

<style scoped>
.metric-card-grid.compact {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 16px;
}

.metric-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.compact-card :deep(.ant-card-body) {
  min-height: 112px;
  padding: 18px 20px;
}

.metric-label {
  color: #64748b;
  font-size: 13px;
}

.metric-value {
  margin-top: 8px;
  color: #0f172a;
  font-size: 26px;
  font-weight: 800;
}

.metric-extra {
  margin-top: 6px;
  color: #94a3b8;
  font-size: 12px;
}
</style>
