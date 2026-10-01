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
      <a-tooltip title="帮助">
        <button
          class="help-trigger"
          type="button"
          aria-label="打开帮助"
          @click="$emit('open-help')"
        >
          <QuestionCircleOutlined />
        </button>
      </a-tooltip>
      <a-tooltip :title="hasNewAnnouncements ? '有新公告' : '公告'">
        <button
          class="announcement-trigger"
          :class="{ active: hasNewAnnouncements, shaking: announcementBellShaking }"
          type="button"
          aria-label="打开公告"
          @click="$emit('open-announcements')"
        >
          <span class="announcement-icon" aria-hidden="true">
            <BellOutlined />
          </span>
          <span v-if="hasNewAnnouncements" class="announcement-dot" />
        </button>
      </a-tooltip>
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
            <a-menu-item key="profile">个人信息</a-menu-item>
            <a-menu-divider />
            <a-menu-item v-if="canSwitchMenuMode" key="switch-mode">
              {{ switchMenuModeLabel }}
            </a-menu-item>
            <a-menu-divider v-if="canSwitchMenuMode" />
            <a-menu-item key="logout" danger>退出登录</a-menu-item>
          </a-menu>
        </template>
      </a-dropdown>
    </a-space>
  </a-layout-header>
</template>

<script setup lang="ts">
import { BellOutlined, DownOutlined, MenuOutlined, QuestionCircleOutlined } from '@ant-design/icons-vue'
import type { MenuProps } from 'ant-design-vue'

defineProps<{
  announcementBellShaking: boolean
  canSwitchMenuMode: boolean
  description: string
  hasNewAnnouncements: boolean
  isMobile: boolean
  switchMenuModeLabel: string
  title: string
  userAvatarText: string
  userDisplayName: string
  userRoleLabel: string
}>()

defineEmits<{
  (event: 'open-help'): void
  (event: 'open-announcements'): void
  (event: 'open-sidebar'): void
  (event: 'user-menu-click', menuEvent: Parameters<NonNullable<MenuProps['onClick']>>[0]): void
}>()
</script>

<style scoped>
.header {
  height: 52px;
  min-height: 52px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 0 20px;
  line-height: normal;
  background: rgba(244, 244, 242, 0.88);
  backdrop-filter: blur(10px);
  border-bottom: 1px solid var(--juhe-border);
  z-index: 2;
}

.header-actions {
  flex: 0 0 auto;
}

.help-trigger,
.announcement-trigger {
  position: relative;
  width: 40px;
  height: 40px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--juhe-muted);
  background: transparent;
  border: 0;
  border-radius: 50%;
  cursor: pointer;
  transition:
    color 0.2s ease,
    background 0.2s ease,
    transform 0.2s ease;
}

.help-trigger:hover,
.help-trigger:focus-visible,
.announcement-trigger:hover,
.announcement-trigger:focus-visible {
  color: var(--juhe-accent);
  background: var(--juhe-accent-soft);
}

.help-trigger:focus-visible,
.announcement-trigger:focus-visible {
  outline: 2px solid rgba(83, 105, 107, 0.28);
  outline-offset: 2px;
}

.help-trigger {
  font-size: 20px;
  line-height: 1;
}

.announcement-trigger.active {
  color: var(--juhe-warn);
  background: transparent;
  box-shadow: none;
}

.announcement-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 20px;
  line-height: 1;
  transform-origin: 50% 10%;
  will-change: transform;
}

.announcement-trigger.shaking {
  transform-origin: center;
}

.announcement-trigger.active .announcement-icon,
.announcement-trigger.shaking .announcement-icon {
  animation: announcement-bell-shake 0.9s ease-in-out infinite;
}

.announcement-dot {
  position: absolute;
  top: 8px;
  right: 8px;
  width: 8px;
  height: 8px;
  background: var(--juhe-coral);
  border: 2px solid #f4f4f2;
  border-radius: 50%;
  box-shadow: 0 0 0 2px rgba(166, 117, 94, 0.18);
}

.user-trigger {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  min-height: 36px;
  padding: 2px 2px;
  color: var(--juhe-fg);
  background: transparent;
  border: 0;
  cursor: pointer;
}

@keyframes announcement-bell-shake {
  0% {
    transform: rotate(0);
  }

  12% {
    transform: rotate(-24deg) translateX(-1px);
  }

  24% {
    transform: rotate(22deg) translateX(1px);
  }

  36% {
    transform: rotate(-18deg) translateX(-1px);
  }

  48% {
    transform: rotate(14deg) translateX(1px);
  }

  60% {
    transform: rotate(-8deg);
  }

  72%,
  100% {
    transform: rotate(0);
  }
}

.user-trigger:hover .user-name,
.user-trigger:focus-visible .user-name {
  color: var(--juhe-accent);
}

.user-trigger:focus-visible {
  outline: 2px solid rgba(83, 105, 107, 0.28);
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
  font-weight: 600;
  line-height: 1;
  background: var(--juhe-primary-grad);
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
  color: var(--juhe-fg);
  font-size: 14px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
  transition: color 0.2s ease;
}

.user-role {
  margin-top: 3px;
  color: var(--juhe-muted);
  font-size: 11px;
}

.user-arrow {
  color: var(--juhe-faint);
  font-size: 11px;
}

.header-copy {
  display: flex;
  align-items: center;
  gap: 12px;
  line-height: 1.2;
  flex: 1 1 auto;
  min-width: 0;
}

.header-copy :deep(.ant-space-item:last-child) {
  min-width: 0;
}

.title {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  color: var(--juhe-fg);
  font-size: 15px;
  font-weight: 650;
  line-height: 20px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.subtitle {
  display: none;
  overflow: hidden;
  color: var(--juhe-muted);
  font-size: 12px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.menu-trigger {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 40px;
  height: 40px;
  margin-left: -8px;
  color: var(--juhe-fg);
}

@media (max-width: 991px) {
  .header {
    height: auto;
    min-height: 56px;
    padding: 0 12px;
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

  .subtitle {
    display: block;
  }
}

@media (max-width: 768px) {
  .header-copy {
    gap: 8px;
  }
}
</style>
