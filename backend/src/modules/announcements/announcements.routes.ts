import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import {
  createAnnouncement,
  deleteAnnouncement,
  listAnnouncements,
  listPublicAnnouncements,
  markPublicAnnouncementsRead,
  publishAnnouncement,
  unpublishAnnouncement,
  updateAnnouncement
} from '../../storage/repositories.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAuthContext } from '../auth/request-context.js'

export const announcementsRouter = Router()

const publicListQuerySchema = z.object({
  limit: z.coerce.number().int().min(1).max(30).optional()
})

const readAnnouncementsSchema = z.object({
  announcementIds: z.array(z.string().trim().min(1)).max(30)
})

const createAnnouncementSchema = z.object({
  title: z.string().trim().min(1).max(120),
  content: z.string().trim().min(1).max(5000),
  level: z.enum(['critical', 'warning', 'info', 'normal']).optional(),
  status: z.enum(['draft', 'published', 'archived']).optional()
})

const updateAnnouncementSchema = z.object({
  title: z.string().trim().min(1).max(120).optional(),
  content: z.string().trim().min(1).max(5000).optional(),
  level: z.enum(['critical', 'warning', 'info', 'normal']).optional(),
  status: z.enum(['draft', 'published', 'archived']).optional()
})

announcementsRouter.get('/public', (req, res) => {
  const parsed = publicListQuerySchema.safeParse(req.query)
  if (!parsed.success) {
    res.status(400).json(badRequest('公告查询参数无效'))
    return
  }
  res.json(ok(listPublicAnnouncements(requireActor(), parsed.data.limit)))
})

announcementsRouter.post('/public/read', (req, res) => {
  const parsed = readAnnouncementsSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('公告已读参数无效'))
    return
  }
  res.json(ok(markPublicAnnouncementsRead(requireActor(), parsed.data.announcementIds)))
})

announcementsRouter.get('/', requireAdmin, (_req, res) => {
  res.json(ok(listAnnouncements()))
})

announcementsRouter.post('/', requireAdmin, (req, res) => {
  const parsed = createAnnouncementSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('公告参数无效'))
    return
  }
  res.status(201).json(ok(createAnnouncement(parsed.data, requireActor())))
})

announcementsRouter.patch('/:id', requireAdmin, (req, res) => {
  const parsed = updateAnnouncementSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('公告参数无效'))
    return
  }
  const announcement = updateAnnouncement(req.params.id, parsed.data, requireActor())
  if (!announcement) {
    res.status(404).json({ message: '公告不存在' })
    return
  }
  res.json(ok(announcement))
})

announcementsRouter.post('/:id/publish', requireAdmin, (req, res) => {
  const announcement = publishAnnouncement(req.params.id, requireActor())
  if (!announcement) {
    res.status(404).json({ message: '公告不存在' })
    return
  }
  res.json(ok(announcement))
})

announcementsRouter.post('/:id/unpublish', requireAdmin, (req, res) => {
  const announcement = unpublishAnnouncement(req.params.id, requireActor())
  if (!announcement) {
    res.status(404).json({ message: '公告不存在' })
    return
  }
  res.json(ok(announcement))
})

announcementsRouter.delete('/:id', requireAdmin, (req, res) => {
  if (!deleteAnnouncement(req.params.id)) {
    res.status(404).json({ message: '公告不存在' })
    return
  }
  res.status(204).send()
})

function requireActor(): string {
  const context = getRequestAuthContext()
  if (!context) {
    throw new Error('请先登录')
  }
  return context.systemAccountId
}
