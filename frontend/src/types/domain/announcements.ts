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

export type PublishedAnnouncementSummary = AnnouncementSummary & {
  publishedAt: string
}

export interface AnnouncementListResult {
  items: AnnouncementSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}
