<template>
  <router-view v-if="$route.meta.public" />
  <AppLayout v-else />
</template>

<script setup lang="ts">
import AppLayout from './layouts/AppLayout.vue'
</script>
