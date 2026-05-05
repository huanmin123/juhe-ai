<template>
  <a-tag :color="color">
    <slot>{{ label }}</slot>
  </a-tag>
</template>

<script setup lang="ts">
defineProps<{
  label?: string
  color?: string
}>()
</script>
