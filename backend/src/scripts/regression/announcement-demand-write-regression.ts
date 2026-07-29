import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-announcement-demand-write-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'announcement-demand-write-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const actor = 'sys_admin'

try {
  const created = await repositories.createAnnouncementForManagementAsync({
    title: '公告字段级写入回归',
    content: '初始正文',
    level: 'info',
    status: 'published'
  }, actor)
  const id = created.receipt.id
  const initialRevision = created.receipt.revision
  const database = databaseModule.getBusinessDatabase()
  database.prepare(`
    INSERT INTO announcement_reads (announcement_id, system_account_id, read_at)
    VALUES (?, ?, ?)
  `).run(id, actor, initialRevision)
  const changesBeforeDuplicateRead = totalChanges(database)
  const duplicateRead = repositories.markPublicAnnouncementsRead(actor, [id, id])
  assert.equal(duplicateRead.count, 0, '重复标记已读必须报告零写入')
  assert.equal(totalChanges(database), changesBeforeDuplicateRead, '重复标记已读必须为零 DML')
  installColumnAuditTriggers(database)

  const changesBeforeNoOp = totalChanges(database)
  const noOp = await repositories.patchAnnouncementForManagementAsync(
    id,
    { title: '公告字段级写入回归' },
    actor,
    initialRevision
  )
  assert.equal(noOp?.changed, false, '同值 PATCH 必须判定为 no-op')
  assert.equal(noOp?.receipt.revision, initialRevision, '同值 PATCH 不得推进 revision')
  assert.equal(totalChanges(database), changesBeforeNoOp, '同值 PATCH 必须为零 DML')
  assert.deepEqual(auditedColumns(database), [], '同值 PATCH 不得命中任何 UPDATE 列')
  assert.equal(readCount(database, id), 1, '同值 PATCH 不得清理公告已读状态')

  const patched = await repositories.patchAnnouncementForManagementAsync(
    id,
    { title: '公告字段级写入回归-已修改' },
    actor,
    initialRevision
  )
  assert.equal(patched?.changed, true, '真实字段变化必须执行写入')
  assert.notEqual(patched?.receipt.revision, initialRevision, '真实字段变化必须推进 revision')
  assert.deepEqual(
    auditedColumns(database),
    ['title', 'updated_at', 'updated_by'].sort(),
    '标题 PATCH 只能更新标题、更新人和 revision'
  )
  assert.equal(readCount(database, id), 1, '发布态普通编辑不得清理已读状态')

  await assert.rejects(
    repositories.patchAnnouncementForManagementAsync(id, { level: 'warning' }, actor, initialRevision),
    (error: unknown) => error instanceof repositories.AnnouncementRevisionConflictError,
    '旧 revision 必须得到 CAS 冲突'
  )

  const archived = await repositories.unpublishAnnouncementForManagementAsync(
    id,
    actor,
    patched!.receipt.revision
  )
  assert.equal(archived?.after?.status, 'archived', '下线应进入 archived')
  assert.equal(readCount(database, id), 1, '下线不应无意义清理已读状态')

  const republished = await repositories.publishAnnouncementForManagementAsync(
    id,
    actor,
    archived!.receipt.revision
  )
  assert.equal(republished?.after?.status, 'published', '重新发布应恢复 published')
  assert.equal(readCount(database, id), 0, '只有非发布态切换到 published 才清理已读状态')

  console.log('公告字段级写入回归通过：动态列、no-op、revision CAS 和已读副作用均按需执行')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function readCount(database: ReturnType<typeof databaseModule.getBusinessDatabase>, announcementId: string): number {
  const row = database.prepare(`
    SELECT COUNT(*) AS count
    FROM announcement_reads
    WHERE announcement_id = ?
  `).get(announcementId) as unknown as { count: number }
  return Number(row.count)
}

function installColumnAuditTriggers(database: ReturnType<typeof databaseModule.getBusinessDatabase>): void {
  database.exec(`
    CREATE TEMP TABLE announcement_update_audit (column_name TEXT NOT NULL);
    CREATE TEMP TRIGGER announcement_update_title AFTER UPDATE OF title ON announcements
      BEGIN INSERT INTO announcement_update_audit VALUES ('title'); END;
    CREATE TEMP TRIGGER announcement_update_content AFTER UPDATE OF content ON announcements
      BEGIN INSERT INTO announcement_update_audit VALUES ('content'); END;
    CREATE TEMP TRIGGER announcement_update_level AFTER UPDATE OF level ON announcements
      BEGIN INSERT INTO announcement_update_audit VALUES ('level'); END;
    CREATE TEMP TRIGGER announcement_update_status AFTER UPDATE OF status ON announcements
      BEGIN INSERT INTO announcement_update_audit VALUES ('status'); END;
    CREATE TEMP TRIGGER announcement_update_published_at AFTER UPDATE OF published_at ON announcements
      BEGIN INSERT INTO announcement_update_audit VALUES ('published_at'); END;
    CREATE TEMP TRIGGER announcement_update_updated_by AFTER UPDATE OF updated_by ON announcements
      BEGIN INSERT INTO announcement_update_audit VALUES ('updated_by'); END;
    CREATE TEMP TRIGGER announcement_update_updated_at AFTER UPDATE OF updated_at ON announcements
      BEGIN INSERT INTO announcement_update_audit VALUES ('updated_at'); END;
  `)
}

function auditedColumns(database: ReturnType<typeof databaseModule.getBusinessDatabase>): string[] {
  return (database.prepare('SELECT column_name FROM announcement_update_audit ORDER BY column_name').all() as unknown as Array<{ column_name: string }>)
    .map((row) => row.column_name)
}

function totalChanges(database: ReturnType<typeof databaseModule.getBusinessDatabase>): number {
  const row = database.prepare('SELECT total_changes() AS value').get() as unknown as { value: number }
  return Number(row.value)
}
