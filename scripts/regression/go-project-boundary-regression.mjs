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
// 受控例外（baseline amendment 2026-09-04，用户批准；见
// projects/maintenance/bootstrap/bootstrap.go 头注释与 gateway go.mod 记载）：
// gateway 组合根在 SQLite 模式启动时必须执行与 Node db-service 相同的六库
// ensure+seed，唯一允许的跨项目 import 是 maintenance 的受控导出面
// backend-go-maintenance/bootstrap（该包只暴露存储引导入口、无自身业务逻辑）。
const crossProjectImportAllowlist = [
  { project: 'gateway', allow: /backend-go-maintenance\/bootstrap\b/g },
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
      assert.doesNotMatch(checked, new RegExp(`backend-go-${other.replace('-', '\\-')}`), `${relative(repoRoot, file)} imports ${other}`);
    }
  }
}

const contractsRoot = join(goRoot, 'shared', 'contracts');
assert.ok(existsSync(join(contractsRoot, 'go.mod')), 'shared contracts go.mod is required');
for (const file of goFiles(contractsRoot)) {
  const source = read(file);
  for (const project of projects) {
    assert.doesNotMatch(source, new RegExp(`backend-go-${project.replace('-', '\\-')}`), `shared contracts imports ${project}`);
  }
}

const platformRoot = join(goRoot, 'shared', 'platform');
assert.ok(existsSync(join(platformRoot, 'go.mod')), 'shared platform go.mod is required');
for (const file of goFiles(platformRoot)) {
  const source = read(file);
  for (const project of projects) {
    assert.doesNotMatch(source, new RegExp(`backend-go-${project.replace('-', '\\-')}`), `shared platform imports ${project}`);
  }
}

assert.ok(!existsSync(join(goRoot, 'internal')), 'backend-go/internal must not retain business packages');
assert.ok(!existsSync(join(goRoot, 'cmd', 'juhe-ai-go-sidecar')), 'legacy Go sidecar command must not remain');

console.log(`go project boundaries ok: ${projects.join(', ')}`);
