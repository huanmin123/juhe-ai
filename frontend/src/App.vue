<template>
  <a-config-provider :locale="zhCN">
    <router-view v-if="$route.meta.public" />
    <AppLayout v-else />
  </a-config-provider>
</template>

<script setup lang="ts">
import zhCN from 'ant-design-vue/es/locale/zh_CN'
import dayjs from 'dayjs'
import 'dayjs/locale/zh-cn'

import AppLayout from './layouts/AppLayout.vue'

dayjs.locale('zh-cn')
</script>
