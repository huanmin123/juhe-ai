<template>
  <a-layout-header class="header">
    <a-space align="center" class="header-copy">
      <a-button v-if="isMobile" type="text" class="menu-trigger" @click="$emit('open-sidebar')">
        <MenuOutlined />
      </a-button>
      <div>
        <div class="title">{{ title }}</div>
        <div class="subtitle">{{ description }}</div>
      </div>
    </a-space>
    <a-space class="header-actions" align="center">
      <a-dropdown :trigger="['click']">
        <button class="user-trigger" type="button" aria-label="打开用户菜单">
          <span class="user-avatar">{{ userAvatarText }}</span>
          <span class="user-meta">
            <span class="user-name">{{ userDisplayName }}</span>
            <span class="user-role">{{ userRoleLabel }}</span>
          </span>
          <DownOutlined class="user-arrow" />
        </button>
        <template #overlay>
          <a-menu @click="$emit('user-menu-click', $event)">
            <a-menu-item key="password">修改密码</a-menu-item>
            <a-menu-item key="logout" danger>退出登录</a-menu-item>
          </a-menu>
        </template>
      </a-dropdown>
    </a-space>
  </a-layout-header>
</template>

<script setup lang="ts">
import { DownOutlined, MenuOutlined } from '@ant-design/icons-vue'
import type { MenuProps } from 'ant-design-vue'

defineProps<{
  description: string
  isMobile: boolean
  title: string
  userAvatarText: string
  userDisplayName: string
  userRoleLabel: string
}>()

defineEmits<{
  (event: 'open-sidebar'): void
  (event: 'user-menu-click', menuEvent: Parameters<NonNullable<MenuProps['onClick']>>[0]): void
}>()
</script>

<style scoped>
.header {
  min-height: 92px;
  height: auto;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 18px 28px 16px;
  line-height: normal;
  background: rgba(255, 255, 255, 0.96);
  border-bottom: 1px solid #edf1f7;
  box-shadow: 0 8px 24px rgba(15, 23, 42, 0.04);
  z-index: 2;
}

.header-actions {
  flex: 0 0 auto;
}

.user-trigger {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  min-height: 44px;
  padding: 2px 2px;
  color: #0f172a;
  background: transparent;
  border: 0;
  cursor: pointer;
}

.user-trigger:hover .user-name,
.user-trigger:focus-visible .user-name {
  color: #1677ff;
}

.user-trigger:focus-visible {
  outline: 2px solid rgba(22, 119, 255, 0.28);
  outline-offset: 4px;
  border-radius: 10px;
}

.user-avatar {
  width: 34px;
  height: 34px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 34px;
  color: #fff;
  font-size: 13px;
  font-weight: 700;
  line-height: 1;
  background: #14b8a6;
  border-radius: 50%;
}

.user-meta {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  min-width: 0;
  line-height: 1.15;
}

.user-name {
  max-width: 120px;
  overflow: hidden;
  color: #0f172a;
  font-size: 14px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
  transition: color 0.2s ease;
}

.user-role {
  margin-top: 3px;
  color: #64748b;
  font-size: 12px;
}

.user-arrow {
  color: #94a3b8;
  font-size: 11px;
}

.header-copy {
  display: flex;
  align-items: center;
  gap: 12px;
  line-height: 1.2;
}

.title {
  color: #0f172a;
  font-size: 20px;
  font-weight: 800;
  line-height: 28px;
}

.subtitle {
  color: #64748b;
  font-size: 13px;
  line-height: 20px;
}

.menu-trigger {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 40px;
  height: 40px;
  margin-left: -8px;
  color: #0f172a;
}

@media (max-width: 991px) {
  .header {
    min-height: 76px;
    padding: 12px 16px;
  }

  .header-copy {
    align-items: center;
    width: 100%;
  }

  .header-actions {
    margin-left: auto;
  }

  .user-meta {
    display: none;
  }

  .title {
    font-size: 18px;
    line-height: 26px;
  }

  .subtitle {
    font-size: 12px;
    line-height: 18px;
  }
}

@media (max-width: 768px) {
  .header-copy {
    gap: 8px;
  }
}
</style>
