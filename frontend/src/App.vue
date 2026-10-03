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
    // 控件选中/悬浮洗底统一为主题低饱和中性色（样式规范 §6.1）：
    // 默认值 controlItemBgActive=主色浅调（灰绿重块，下拉选中项/菜单选中/日历
    // 激活等所有控件选中态共用），视觉过重；controlItemBgHover=三级填充偏灰。
    // 此处覆盖为「选中洗底 #edf1f0」一族，悬浮在其上再深半档。
    controlItemBgActive: '#edf1f0',
    controlItemBgActiveHover: '#e4eae8',
    controlItemBgHover: '#f6f8f7',
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
