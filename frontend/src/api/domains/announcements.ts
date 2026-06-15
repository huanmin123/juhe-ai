import type { AnnouncementListResult, AnnouncementSummary, PublishedAnnouncementSummary } from '@/types/domain'
import type { AnnouncementListParams, AnnouncementPayload, AnnouncementReadResult } from '../contracts'
import { http, unwrap } from '../http'

export const announcementsApi = {
  publicList: (params?: AnnouncementListParams) => unwrap<PublishedAnnouncementSummary[]>(http.get('/announcements/public', { params })),
  markRead: (payload: { announcementIds: string[] }) => unwrap<AnnouncementReadResult>(http.post('/announcements/public/read', payload)),
  list: async () => (await unwrap<AnnouncementListResult>(http.get('/announcements', { params: { page: 1, pageSize: 100 } }))).items,
  listPage: (params?: { page?: number; pageSize?: number }) => unwrap<AnnouncementListResult>(http.get('/announcements', { params })),
  detail: (id: string) => unwrap<AnnouncementSummary>(http.get(`/announcements/${id}`)),
  create: (payload: AnnouncementPayload) => unwrap<AnnouncementSummary>(http.post('/announcements', payload)),
  update: (id: string, payload: Partial<AnnouncementPayload>) => unwrap<AnnouncementSummary>(http.patch(`/announcements/${id}`, payload)),
  publish: (id: string) => unwrap<AnnouncementSummary>(http.post(`/announcements/${id}/publish`)),
  unpublish: (id: string) => unwrap<AnnouncementSummary>(http.post(`/announcements/${id}/unpublish`)),
  delete: (id: string) => http.delete(`/announcements/${id}`)
}
