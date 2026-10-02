import type { AnnouncementLevel, AnnouncementStatus } from '@/types/domain'

export function announcementLevelText(level: AnnouncementLevel): string {
  if (level === 'critical') return '重要'
  if (level === 'warning') return '提醒'
  if (level === 'normal') return '普通'
  return '通知'
}

export function announcementLevelColor(level: AnnouncementLevel): string {
  if (level === 'critical') return 'red'
  if (level === 'warning') return 'orange'
  if (level === 'normal') return 'default'
  return 'blue'
}

// AntD a-tag :color 为 JS 消费，须为字面量，色值镜像主题令牌（danger/warn/faint/accent）
export function announcementTimelineColor(level: AnnouncementLevel): string {
  if (level === 'critical') return '#9d5547'
  if (level === 'warning') return '#a98548'
  if (level === 'normal') return '#a9aeac'
  return '#53696b'
}

export function announcementStatusText(status: AnnouncementStatus): string {
  if (status === 'published') return '已发布'
  if (status === 'archived') return '已下线'
  return '草稿'
}

export function announcementStatusColor(status: AnnouncementStatus): string {
  if (status === 'published') return 'green'
  if (status === 'archived') return 'default'
  return 'gold'
}
