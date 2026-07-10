<template>
  <section class="batch-edit-field" :class="{ enabled: checked, disabled: disabled }">
    <div class="batch-edit-field-header">
      <a-checkbox v-model:checked="checked" :disabled="disabled">
        <strong>{{ label }}</strong>
      </a-checkbox>
      <span v-if="description" class="batch-edit-field-description">{{ description }}</span>
    </div>
    <div class="batch-edit-field-control">
      <slot :disabled="disabled || !checked" />
    </div>
  </section>
</template>

<script setup lang="ts">
const checked = defineModel<boolean>('checked', { required: true })

withDefaults(defineProps<{
  description?: string
  disabled?: boolean
  label: string
}>(), {
  description: '',
  disabled: false
})
</script>

<style scoped>
.batch-edit-field {
  display: grid;
  grid-template-columns: minmax(180px, 220px) minmax(0, 1fr);
  gap: 16px;
  padding: 14px 0;
  border-bottom: 1px solid #eef2f7;
}

.batch-edit-field:last-child {
  border-bottom: 0;
}

.batch-edit-field-header {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
}

.batch-edit-field-description {
  padding-left: 24px;
  color: #64748b;
  font-size: 12px;
  line-height: 1.5;
}

.batch-edit-field-control {
  min-width: 0;
}

.batch-edit-field.disabled {
  opacity: 0.65;
}

@media (max-width: 720px) {
  .batch-edit-field {
    grid-template-columns: 1fr;
    gap: 8px;
  }
}
</style>
