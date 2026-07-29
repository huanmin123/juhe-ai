import type {
  AnnouncementListResult,
  AnnouncementEditDetail,
  AnnouncementMutationResult,
  PublishedAnnouncementDetail,
  PublishedAnnouncementListItem
} from '@/types/domain'
import type { AnnouncementListParams, AnnouncementPatchPayload, AnnouncementPayload, AnnouncementReadResult, AnnouncementVersionPayload } from '../contracts'
import { http, unwrap } from '../http'

export const announcementsApi = {
  publicList: (params?: AnnouncementListParams) => unwrap<PublishedAnnouncementListItem[]>(http.get('/announcements/public', { params })),
  publicDetail: (id: string) => unwrap<PublishedAnnouncementDetail>(http.get(`/announcements/public/${id}`)),
  markRead: (payload: { announcementIds: string[] }) => unwrap<AnnouncementReadResult>(http.post('/announcements/public/read', payload)),
  list: async () => (await unwrap<AnnouncementListResult>(http.get('/announcements', { params: { page: 1, pageSize: 100 } }))).items,
  listPage: (params?: { page?: number; pageSize?: number }) => unwrap<AnnouncementListResult>(http.get('/announcements', { params })),
  detail: (id: string) => unwrap<AnnouncementEditDetail>(http.get(`/announcements/${id}`)),
  create: (payload: AnnouncementPayload) => unwrap<AnnouncementMutationResult>(http.post('/announcements', payload)),
  update: (id: string, payload: AnnouncementPatchPayload) => unwrap<AnnouncementMutationResult>(http.patch(`/announcements/${id}`, payload)),
  publish: (id: string, payload: AnnouncementVersionPayload) => unwrap<AnnouncementMutationResult>(http.post(`/announcements/${id}/publish`, payload)),
  unpublish: (id: string, payload: AnnouncementVersionPayload) => unwrap<AnnouncementMutationResult>(http.post(`/announcements/${id}/unpublish`, payload)),
  delete: (id: string, payload: AnnouncementVersionPayload) => http.delete(`/announcements/${id}`, { data: payload })
}
