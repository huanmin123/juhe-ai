import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const repository = readFileSync(resolve('src/storage/system-team.repository.ts'), 'utf8')
const routes = readFileSync(resolve('src/modules/system-teams/system-teams.routes.ts'), 'utf8')

const listMapper = functionBody(repository, 'systemTeamListItemFromRow')
assert.match(listMapper, /memberCount/, '系统团队列表必须保留成员数')
for (const field of ['activeMemberCount', 'createdBy', 'updatedAt']) {
  assert.doesNotMatch(listMapper, new RegExp(`\\b${field}\\b`), `系统团队列表不得返回未使用字段 ${field}`)
}

assert.match(repository, /SystemTeamListItem/, '系统团队列表必须使用独立轻量 DTO')
assert.match(routes, /findSystemTeamDetailAsync/, '系统团队详情必须使用按需详情读取，不复用列表摘要')
assert.match(routes, /compactSystemTeamResult/, '系统团队写接口必须投影轻量响应，避免内部摘要回流前端')

const memberMapper = functionBody(repository, 'systemTeamMemberDetailFromRow')
assert.match(memberMapper, /id:/, '成员详情必须返回成员 ID')
assert.match(memberMapper, /systemAccountId:/, '成员详情必须返回系统账户 ID')
assert.match(memberMapper, /systemAccountName:/, '成员详情必须返回系统账户名称')
assert.match(memberMapper, /joinedAt:/, '成员详情必须返回加入时间')
for (const field of ['teamId', 'username', 'memberRole', 'status', 'removedAt', 'createdAt', 'updatedAt']) {
  assert.doesNotMatch(memberMapper, new RegExp(`\\b${field}\\b`), `成员详情不得返回未使用字段 ${field}`)
}

console.log('系统团队轻量列表与成员详情契约回归通过')

function functionBody(source: string, name: string): string {
  const start = source.indexOf(`function ${name}`)
  assert.notEqual(start, -1, `缺少函数 ${name}`)
  const open = source.indexOf('{', start)
  let depth = 0
  for (let index = open; index < source.length; index += 1) {
    if (source[index] === '{') depth += 1
    if (source[index] === '}' && --depth === 0) return source.slice(open, index + 1)
  }
  throw new Error(`无法解析函数 ${name}`)
}
