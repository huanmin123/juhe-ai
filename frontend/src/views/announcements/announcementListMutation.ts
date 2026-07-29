import type {
  AnnouncementLevel,
  AnnouncementListItem,
  AnnouncementMutationResult,
  AnnouncementStatus
} from '@/types/domain'

export interface AnnouncementListMutationState {
  items: AnnouncementListItem[]
  totalDelta: number
}

export interface AnnouncementListMutationWindow {
  currentPage: number
  pageSize: number
}

export interface AnnouncementListCreateValues {
  title: string
  content: string
  level: AnnouncementLevel
  status: AnnouncementStatus
}

export type AnnouncementListPatchValues = Partial<AnnouncementListCreateValues>

export function applyAnnouncementCreateMutation(
  items: AnnouncementListItem[],
  receipt: AnnouncementMutationResult,
  values: AnnouncementListCreateValues,
  updatedByName: string | undefined,
  window: AnnouncementListMutationWindow
): AnnouncementListMutationState {
  const accumulated = window.currentPage > 1 && items.length > window.pageSize
  if (window.currentPage > 1 && !accumulated) {
    return { items, totalDelta: 1 }
  }
  const created = announcementListItemFromValues(receipt, values, updatedByName)
  const limit = accumulated ? window.currentPage * window.pageSize : window.pageSize
  return {
    items: [created, ...items.filter((item) => item.id !== created.id)].slice(0, limit),
    totalDelta: 1
  }
}

export function applyAnnouncementPatchMutation(
  items: AnnouncementListItem[],
  receipt: AnnouncementMutationResult,
  values: AnnouncementListPatchValues,
  updatedByName: string | undefined,
  window: AnnouncementListMutationWindow
): AnnouncementListMutationState {
  const existing = items.find((item) => item.id === receipt.id)
  if (!existing) return { items, totalDelta: 0 }
  const content = values.content
  const preview = content === undefined ? undefined : announcementContentPreview(content)
  const nextStatus = values.status ?? existing.status
  const becamePublished = nextStatus === 'published' && existing.status !== 'published'
  const updated: AnnouncementListItem = {
    ...existing,
    ...(values.title === undefined ? {} : { title: values.title }),
    ...(preview === undefined ? {} : { contentPreview: preview.contentPreview, contentTruncated: preview.contentTruncated }),
    ...(values.level === undefined ? {} : { level: values.level }),
    status: nextStatus,
    updatedByName: updatedByName ?? existing.updatedByName,
    publishedAt: becamePublished ? receipt.revision : existing.publishedAt,
    revision: receipt.revision
  }
  const accumulated = window.currentPage > 1 && items.length > window.pageSize
  if (window.currentPage > 1 && !accumulated) {
    return { items: items.filter((item) => item.id !== receipt.id), totalDelta: 0 }
  }
  const limit = accumulated ? window.currentPage * window.pageSize : window.pageSize
  return {
    items: [updated, ...items.filter((item) => item.id !== receipt.id)].slice(0, limit),
    totalDelta: 0
  }
}

export function applyAnnouncementDeleteMutation(
  items: AnnouncementListItem[],
  id: string
): AnnouncementListMutationState {
  const exists = items.some((item) => item.id === id)
  return {
    items: items.filter((item) => item.id !== id),
    totalDelta: exists ? -1 : 0
  }
}

export function announcementContentPreview(content: string): Pick<AnnouncementListItem, 'contentPreview' | 'contentTruncated'> {
  const characters = Array.from(content)
  return characters.length > 240
    ? { contentPreview: `${characters.slice(0, 240).join('')}...`, contentTruncated: true }
    : { contentPreview: content, contentTruncated: false }
}

function announcementListItemFromValues(
  receipt: AnnouncementMutationResult,
  values: AnnouncementListCreateValues,
  updatedByName: string | undefined
): AnnouncementListItem {
  return {
    id: receipt.id,
    title: values.title,
    ...announcementContentPreview(values.content),
    level: values.level,
    status: values.status,
    updatedByName,
    publishedAt: values.status === 'published' ? receipt.revision : undefined,
    revision: receipt.revision
  }
}
