export type AnnouncementLevel = 'critical' | 'warning' | 'info' | 'normal'
export type AnnouncementStatus = 'draft' | 'published' | 'archived'

export interface AnnouncementSummary {
  id: string
  title: string
  content: string
  level: AnnouncementLevel
  status: AnnouncementStatus
  createdBy?: string
  createdByName?: string
  updatedBy?: string
  updatedByName?: string
  publishedAt?: string
  readAt?: string
  createdAt: string
  updatedAt: string
}

export interface AnnouncementListItem {
  id: string
  title: string
  contentPreview: string
  level: AnnouncementLevel
  status: AnnouncementStatus
  createdBy?: string
  createdByName?: string
  updatedBy?: string
  updatedByName?: string
  publishedAt?: string
  createdAt: string
  updatedAt: string
}

export interface PublishedAnnouncementListItem {
  id: string
  title: string
  level: AnnouncementLevel
  publishedAt: string
  readAt?: string
}

export interface PublishedAnnouncementDetail {
  id: string
  title: string
  content: string
  level: AnnouncementLevel
  publishedAt: string
}

export interface AnnouncementListResult {
  items: AnnouncementListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface AnnouncementMutationResult {
  id: string
  revision: string
}
