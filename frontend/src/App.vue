<template>
  <a-config-provider :locale="zhCN" :theme="themeConfig">
    <router-view v-if="$route.meta.public" />
    <AppLayout v-else />
  </a-config-provider>
</template>

<script setup lang="ts">
import zhCN from 'ant-design-vue/es/locale/zh_CN'
import dayjs from 'dayjs'
import 'dayjs/locale/zh-cn'

import AppLayout from './layouts/AppLayout.vue'

const themeConfig = {
  token: {
    colorPrimary: '#53696b',
    colorInfo: '#53696b',
    colorSuccess: '#6f8f7a',
    colorWarning: '#a98548',
    colorError: '#9d5547',
    colorTextBase: '#22282b',
    colorBgLayout: '#f4f4f2',
    colorBgContainer: '#ffffff',
    borderRadius: 10,
    fontFamily: '"Helvetica Neue","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif'
  },
  components: {
    Table: { headerBg: '#faf9f7', rowHoverBg: '#f6f8f7' },
    Layout: { siderBg: '#fbfbfa', headerBg: 'transparent' }
  }
}

dayjs.locale('zh-cn')
</script>
