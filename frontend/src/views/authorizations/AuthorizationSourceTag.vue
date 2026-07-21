<template>
  <a-tag v-if="label" :color="color">{{ label }}</a-tag>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { ResourceAuthorizationListItem } from '@/types/domain'
import { granteeSourceLabel, granteeSourceTagColor } from './authorizationFormatters'

const props = defineProps<{
  authorization: ResourceAuthorizationListItem
}>()

const label = computed(() => granteeSourceLabel(props.authorization))
const color = computed(() => granteeSourceTagColor(props.authorization))
</script>
