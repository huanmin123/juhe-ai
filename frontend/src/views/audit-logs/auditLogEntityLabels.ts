import { accountLabelForId, rememberAccountLabel } from '@/shared/accountLabelCache'
import { groupLabelForId, rememberGroupLabel } from '@/shared/groupLabelCache'
import { principalLabelForId, rememberPrincipalLabel } from '@/shared/principalLabelCache'

/**
 * 审计读链路（juhe_dataset 审计库）只返回实体 ID、不返回名称；列表与详情
 * 在前端按 label cache 模式补齐：缓存缺失的 ID 走分组/账户/系统账户的
 * options 批量接口解析，名称写回行对象（响应式，写回即重渲染）并写入各
 * label 缓存供后续页面直接命中。已确认解析不到的 ID（如已删除）记入跳过
 * 集合，避免翻页重复出现时反复请求。
 */
export interface AuditLogEntityLabelRecord {
  accountId?: string
  accountName?: string
  groupId?: string
  groupName?: string
  systemAccountId?: string
  systemAccountName?: string
}

export interface AuditLogEntityNameOption {
  id: string
  name?: string
}

export interface AuditLogEntityLabelApi {
  accountOptions: (ids: string[]) => Promise<AuditLogEntityNameOption[]>
  groupOptions: (ids: string[]) => Promise<AuditLogEntityNameOption[]>
  systemAccountOptions: (ids: string[]) => Promise<AuditLogEntityNameOption[]>
}

interface EntityLabelKind {
  readId: (item: AuditLogEntityLabelRecord) => string | undefined
  hasResolvableName: (id: string) => boolean
  fetchNames: (ids: string[]) => Promise<AuditLogEntityNameOption[]>
  applyName: (item: AuditLogEntityLabelRecord, name: string) => void
  unresolvableIds: Set<string>
}

const maxIdsPerRequest = 50

export function createAuditLogEntityLabelHydrator(entityApi: AuditLogEntityLabelApi): (items: AuditLogEntityLabelRecord[]) => void {
  const kinds: EntityLabelKind[] = [
    {
      readId: (item) => item.groupId,
      hasResolvableName: (id) => Boolean(groupLabelForId(id)),
      fetchNames: (ids) => entityApi.groupOptions(ids),
      applyName: (item, name) => {
        item.groupName = name
        rememberGroupLabel(item.groupId, name)
      },
      unresolvableIds: new Set<string>()
    },
    {
      readId: (item) => item.accountId,
      hasResolvableName: (id) => Boolean(accountLabelForId(id)),
      fetchNames: (ids) => entityApi.accountOptions(ids),
      applyName: (item, name) => {
        item.accountName = name
        rememberAccountLabel(item.accountId, name)
      },
      unresolvableIds: new Set<string>()
    },
    {
      readId: (item) => item.systemAccountId,
      hasResolvableName: (id) => Boolean(principalLabelForId('system_account', id)),
      fetchNames: (ids) => entityApi.systemAccountOptions(ids),
      applyName: (item, name) => {
        item.systemAccountName = name
        rememberPrincipalLabel('system_account', item.systemAccountId, name)
      },
      unresolvableIds: new Set<string>()
    }
  ]

  return function hydrateAuditLogEntityLabels(items: AuditLogEntityLabelRecord[]): void {
    for (const kind of kinds) {
      const ids = collectIdsToResolve(items, kind)
      if (!ids.length) continue
      void kind.fetchNames(ids.slice(0, maxIdsPerRequest)).then(
        (options) => applyResolvedNames(items, kind, ids, options),
        () => undefined
      )
    }
  }
}

function collectIdsToResolve(items: AuditLogEntityLabelRecord[], kind: EntityLabelKind): string[] {
  const ids = new Set<string>()
  for (const item of items) {
    const id = kind.readId(item)?.trim()
    if (!id || kind.hasResolvableName(id) || kind.unresolvableIds.has(id)) continue
    ids.add(id)
  }
  return [...ids]
}

function applyResolvedNames(
  items: AuditLogEntityLabelRecord[],
  kind: EntityLabelKind,
  requestedIds: string[],
  options: AuditLogEntityNameOption[]
): void {
  const nameById = new Map<string, string>()
  for (const option of options) {
    const name = option.name?.trim()
    if (option.id && name) nameById.set(option.id, name)
  }
  for (const id of requestedIds) {
    const name = nameById.get(id)
    if (!name) {
      kind.unresolvableIds.add(id)
      continue
    }
    for (const item of items) {
      if (kind.readId(item)?.trim() === id) kind.applyName(item, name)
    }
  }
}
