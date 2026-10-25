import assert from 'node:assert/strict';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join, relative, resolve } from 'node:path';

const repoRoot = resolve(fileURLToPath(new URL('../..', import.meta.url)));
const goRoot = join(repoRoot, 'backend-go');
const projects = ['gateway', 'jobs', 'maintenance'];

const read = (file) => readFileSync(file, 'utf8');
const goFiles = (root) => {
  const files = [];
  for (const entry of readdirSync(root, { withFileTypes: true })) {
    if (entry.name === 'vendor' || entry.name.startsWith('.')) continue;
    const path = join(root, entry.name);
    if (entry.isDirectory()) files.push(...goFiles(path));
    else if (entry.name.endsWith('.go')) files.push(path);
  }
  return files;
};

assert.ok(existsSync(join(goRoot, 'go.work')), 'backend-go/go.work is required');
const workspace = read(join(goRoot, 'go.work'));
// 受控例外（baseline amendment 2026-09-04，用户批准；登记于 gateway go.mod 与
// maintenance/bootstrap 包注释；2026-10-24/25 存储收口后收窄）：gateway 组合根
// 的运行时 SQLite 六库 ensure+seed 引导已随 SQLite 退役删除（schema 初始化唯一
// 入口是 juhe-ai-maintenance --ensure-schema），当前该例外仅剩测试专用——gateway
// cmd 层测试经 maintenance/bootstrap.EnsurePostgres 为 pgtestkit 隔离库注入 DDL。
// 唯一允许的跨项目 import 仍是受控导出面 backend-go-maintenance/bootstrap（只暴
// 露存储引导入口、无自身业务逻辑）。
// 受控例外 2（2026-10-06，BUG-0182 统计缓存离线重建 CLI；基线 §2 同步补记）：
// maintenance 的 rebuild-usage-stats 子命令必须复用 jobs 的 statsagg 聚合
// 口径（BUG-0182 建议方案 #3 明确禁止第二套口径实现），唯一允许的 import 是
// jobs 的受控导出面 backend-go-jobs/statsrebuild（只暴露重建编排入口，
// 编排内部全部委托 internal/statsagg，无自身口径 SQL）。
// crossProjectImportPattern 锚定完整 import 路径（github.com/huanminabc/
// juhe-ai/backend-go-<project>…）：裸 `backend-go-<project>` 串会命中注释里
// 的模块名描述造成误报（w0cross_contract_golden_test.go 文件头，2026-10-06
// 修正）；Go 源里的跨项目引用必然以完整 module 路径出现。
function crossProjectImportPattern(project) {
  const escaped = project.replace('-', '\\-');
  return new RegExp(`github\\.com\\/huanminabc\\/juhe-ai\\/backend-go-${escaped}\\b`);
}

const crossProjectImportAllowlist = [
  // 匹配锚定完整 import 路径（github.com/huanminabc/juhe-ai/<module>…）：
  // 裸 `backend-go-<project>` 串会命中注释里的模块名描述造成误报
  // （w0cross_contract_golden_test.go 文件头，2026-10-06 修正）。
  { project: 'gateway', allow: /github\.com\/huanminabc\/juhe-ai\/backend-go-maintenance\/bootstrap\b/g },
  { project: 'maintenance', allow: /github\.com\/huanminabc\/juhe-ai\/backend-go-jobs\/statsrebuild\b/g },
];
for (const project of projects) {
  const projectRoot = join(goRoot, 'projects', project);
  assert.ok(existsSync(join(projectRoot, 'go.mod')), `${project} go.mod is required`);
  assert.ok(existsSync(join(projectRoot, 'cmd', `juhe-ai-${project}`, 'main.go')), `${project} command is required`);
  assert.match(workspace, new RegExp(`\\./projects/${project}\\b`), `${project} must be in go.work`);
  const mod = read(join(projectRoot, 'go.mod'));
  assert.match(mod, /backend-go-contracts/, `${project} must use shared contracts`);
  const allowEntry = crossProjectImportAllowlist.find((entry) => entry.project === project);
  for (const file of goFiles(projectRoot)) {
    const source = read(file);
    const checked = allowEntry ? source.replace(allowEntry.allow, '') : source;
    for (const other of projects) {
      if (other === project) continue;
      assert.doesNotMatch(checked, crossProjectImportPattern(other), `${relative(repoRoot, file)} imports ${other}`);
    }
  }
}

const contractsRoot = join(goRoot, 'shared', 'contracts');
assert.ok(existsSync(join(contractsRoot, 'go.mod')), 'shared contracts go.mod is required');
for (const file of goFiles(contractsRoot)) {
  const source = read(file);
  for (const project of projects) {
    assert.doesNotMatch(source, crossProjectImportPattern(project), `shared contracts imports ${project}`);
  }
}

const platformRoot = join(goRoot, 'shared', 'platform');
assert.ok(existsSync(join(platformRoot, 'go.mod')), 'shared platform go.mod is required');
for (const file of goFiles(platformRoot)) {
  const source = read(file);
  for (const project of projects) {
    assert.doesNotMatch(source, crossProjectImportPattern(project), `shared platform imports ${project}`);
  }
}

assert.ok(!existsSync(join(goRoot, 'internal')), 'backend-go/internal must not retain business packages');
assert.ok(!existsSync(join(goRoot, 'cmd', 'juhe-ai-go-sidecar')), 'legacy Go sidecar command must not remain');

console.log(`go project boundaries ok: ${projects.join(', ')}`);
