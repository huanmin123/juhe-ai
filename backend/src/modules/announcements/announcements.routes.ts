import { Router } from 'express'
import { z } from 'zod'

import { badRequest, ok } from '../../shared/http.js'
import {
  createAnnouncementAsync,
  deleteAnnouncementAsync,
  findAnnouncementAsync,
  findPublicAnnouncementAsync,
  listAnnouncementsPageAsync,
  listPublicAnnouncementsAsync,
  markPublicAnnouncementsReadAsync,
  publishAnnouncementAsync,
  unpublishAnnouncementAsync,
  updateAnnouncementAsync
} from '../../storage/repositories.js'
import { publishAnnouncementPublicChange } from '../page-data/page-data-change.publisher.js'
import { requireAdmin } from '../auth/auth.middleware.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { bodyField, mutationGuard, normalizedText, sensitiveFingerprint, textValue } from '../deduplication/mutation-guard.middleware.js'
import { diffSafeFields, runLoggedOperationAsync, safeChange } from '../operation-logs/operation-log.service.js'

export const announcementsRouter = Router()

const publicListQuerySchema = z.object({
  limit: z.coerce.number().int().min(1).max(30).optional()
})

const adminListQuerySchema = z.object({
  page: z.coerce.number().int().min(1).optional(),
  pageSize: z.coerce.number().int().min(1).max(100).optional()
})

const readAnnouncementsSchema = z.object({
  announcementIds: z.array(z.string().trim().min(1)).max(30)
}).strict()

const createAnnouncementSchema = z.object({
  title: z.string().trim().min(1).max(120),
  content: z.string().trim().min(1).max(5000),
  level: z.enum(['critical', 'warning', 'info', 'normal']).optional(),
  status: z.enum(['draft', 'published', 'archived']).optional()
}).strict()

const updateAnnouncementSchema = z.object({
  title: z.string().trim().min(1).max(120).optional(),
  content: z.string().trim().min(1).max(5000).optional(),
  level: z.enum(['critical', 'warning', 'info', 'normal']).optional(),
  status: z.enum(['draft', 'published', 'archived']).optional()
}).strict()

announcementsRouter.get('/public', async (req, res, next) => {
  try {
    const parsed = publicListQuerySchema.safeParse(req.query)
    if (!parsed.success) {
      res.status(400).json(badRequest('公告查询参数无效'))
      return
    }
    res.json(ok(await listPublicAnnouncementsAsync(requireActor(), parsed.data.limit)))
  } catch (error) {
    next(error)
  }
})

announcementsRouter.post('/public/read', async (req, res) => {
  const parsed = readAnnouncementsSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('公告已读参数无效'))
    return
  }
  res.json(ok(await markPublicAnnouncementsReadAsync(requireActor(), parsed.data.announcementIds)))
})

announcementsRouter.get('/public/:id', async (req, res, next) => {
  try {
    const announcement = await findPublicAnnouncementAsync(req.params.id)
    if (!announcement) {
      res.status(404).json({ message: '公告不存在' })
      return
    }
    res.json(ok(announcement))
  } catch (error) {
    next(error)
  }
})

announcementsRouter.get('/', requireAdmin, async (req, res, next) => {
  try {
    const parsed = adminListQuerySchema.safeParse(req.query)
    if (!parsed.success) {
      res.status(400).json(badRequest('公告查询参数无效'))
      return
    }
    res.json(ok(await listAnnouncementsPageAsync(parsed.data)))
  } catch (error) {
    next(error)
  }
})

announcementsRouter.get('/:id', requireAdmin, async (req, res, next) => {
  try {
    const announcement = await findAnnouncementAsync(req.params.id)
    if (!announcement) {
      res.status(404).json({ message: '公告不存在' })
      return
    }
    res.json(ok(announcement))
  } catch (error) {
    next(error)
  }
})

announcementsRouter.post('/', requireAdmin, mutationGuard({
  operationKey: 'announcements.create',
  fingerprint: (req) => ({
    title: normalizedText(bodyField(req, 'title')),
    content: sensitiveFingerprint(bodyField(req, 'content')),
    level: textValue(bodyField(req, 'level')),
    status: textValue(bodyField(req, 'status'))
  })
}), async (req, res) => {
  const parsed = createAnnouncementSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('公告参数无效'))
    return
  }
  const announcement = await runLoggedOperationAsync(async () => {
    const announcement = await createAnnouncementAsync(parsed.data, requireActor())
    return {
      result: announcement,
      log: {
        mode: 'admin',
        module: 'announcements',
        action: 'create',
        operationKey: 'announcements.create',
        resourceType: 'announcement',
        resourceId: announcement.id,
        resourceName: announcement.title,
        summary: `创建公告：${announcement.title}`,
        visibilityScope: announcement.status === 'published' ? 'all_users' : 'admin_only',
        detailLevel: announcement.status === 'published' ? 'summary' : 'full',
        changes: [
          safeChange('title', '标题', undefined, announcement.title),
          safeChange('level', '级别', undefined, announcement.level),
          safeChange('status', '状态', undefined, announcement.status)
        ]
      }
    }
  }, req)
  if (announcement.status === 'published') {
    await publishAnnouncementPublicChange({
      announcementId: announcement.id,
      operation: 'upsert',
      fieldMask: ['title', 'content', 'level', 'status', 'publishedAt']
    })
  }
  res.status(201).json(ok(announcement))
})

announcementsRouter.patch('/:id', requireAdmin, async (req, res) => {
  const parsed = updateAnnouncementSchema.safeParse(req.body)
  if (!parsed.success) {
    res.status(400).json(badRequest('公告参数无效'))
    return
  }
  const before = await findAnnouncementAsync(req.params.id)
  if (!before) {
    res.status(404).json({ message: '公告不存在' })
    return
  }
  const announcement = await runLoggedOperationAsync(async () => {
    const announcement = await updateAnnouncementAsync(req.params.id, parsed.data, requireActor())
    if (!announcement) {
      throw new Error('公告不存在')
    }
    return {
      result: announcement,
      log: {
        mode: 'admin',
        module: 'announcements',
        action: 'update',
        operationKey: 'announcements.update',
        resourceType: 'announcement',
        resourceId: announcement.id,
        resourceName: announcement.title,
        summary: `更新公告：${announcement.title}`,
        visibilityScope: announcement.status === 'published' || before?.status === 'published' ? 'all_users' : 'admin_only',
        detailLevel: announcement.status === 'published' || before?.status === 'published' ? 'summary' : 'full',
        changes: diffSafeFields(before as unknown as Record<string, unknown> | undefined, announcement as unknown as Record<string, unknown>, {
          title: '标题',
          content: '内容',
          level: '级别',
          status: '状态'
        })
      }
    }
  }, req)
  if (announcement.status === 'published' || before.status === 'published') {
    await publishAnnouncementPublicChange({
      announcementId: announcement.id,
      operation: announcement.status === 'published' ? 'upsert' : 'delete',
      fieldMask: ['title', 'content', 'level', 'status', 'publishedAt']
    })
  }
  res.json(ok(announcement))
})

announcementsRouter.post('/:id/publish', requireAdmin, async (req, res) => {
  const before = await findAnnouncementAsync(req.params.id)
  if (!before) {
    res.status(404).json({ message: '公告不存在' })
    return
  }
  const announcement = await runLoggedOperationAsync(async () => {
    const announcement = await publishAnnouncementAsync(req.params.id, requireActor())
    if (!announcement) {
      throw new Error('公告不存在')
    }
    return {
      result: announcement,
      log: {
        mode: 'admin',
        module: 'announcements',
        action: 'publish',
        operationKey: 'announcements.publish',
        resourceType: 'announcement',
        resourceId: announcement.id,
        resourceName: announcement.title,
        summary: `发布公告：${announcement.title}`,
        visibilityScope: 'all_users',
        detailLevel: 'summary',
        changes: diffSafeFields(before as unknown as Record<string, unknown> | undefined, announcement as unknown as Record<string, unknown>, {
          status: '状态',
          publishedAt: '发布时间'
        })
      }
    }
  }, req)
  await publishAnnouncementPublicChange({
    announcementId: announcement.id,
    operation: 'upsert',
    fieldMask: ['status', 'publishedAt']
  })
  res.json(ok(announcement))
})

announcementsRouter.post('/:id/unpublish', requireAdmin, async (req, res) => {
  const before = await findAnnouncementAsync(req.params.id)
  if (!before) {
    res.status(404).json({ message: '公告不存在' })
    return
  }
  const announcement = await runLoggedOperationAsync(async () => {
    const announcement = await unpublishAnnouncementAsync(req.params.id, requireActor())
    if (!announcement) {
      throw new Error('公告不存在')
    }
    return {
      result: announcement,
      log: {
        mode: 'admin',
        module: 'announcements',
        action: 'unpublish',
        operationKey: 'announcements.unpublish',
        resourceType: 'announcement',
        resourceId: announcement.id,
        resourceName: announcement.title,
        summary: `下线公告：${announcement.title}`,
        visibilityScope: 'all_users',
        detailLevel: 'summary',
        changes: diffSafeFields(before as unknown as Record<string, unknown> | undefined, announcement as unknown as Record<string, unknown>, {
          status: '状态'
        })
      }
    }
  }, req)
  await publishAnnouncementPublicChange({
    announcementId: announcement.id,
    operation: 'delete',
    fieldMask: ['status']
  })
  res.json(ok(announcement))
})

announcementsRouter.delete('/:id', requireAdmin, async (req, res) => {
  const before = await findAnnouncementAsync(req.params.id)
  if (!before) {
    res.status(404).json({ message: '公告不存在' })
    return
  }
  await runLoggedOperationAsync(async () => {
    if (!await deleteAnnouncementAsync(req.params.id)) {
      throw new Error('公告不存在')
    }
    return {
      result: true,
      log: {
        mode: 'admin',
        module: 'announcements',
        action: 'delete',
        operationKey: 'announcements.delete',
        resourceType: 'announcement',
        resourceId: req.params.id,
        resourceName: before?.title ?? req.params.id,
        summary: `删除公告：${before?.title ?? req.params.id}`,
        visibilityScope: before?.status === 'published' ? 'all_users' : 'admin_only',
        detailLevel: before?.status === 'published' ? 'summary' : 'full',
        changes: [safeChange('deleted', '删除状态', false, true)]
      }
    }
  }, req)
  if (before.status === 'published') {
    await publishAnnouncementPublicChange({
      announcementId: req.params.id,
      operation: 'delete',
      fieldMask: []
    })
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
