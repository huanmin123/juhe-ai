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
for (const project of projects) {
  const projectRoot = join(goRoot, 'projects', project);
  assert.ok(existsSync(join(projectRoot, 'go.mod')), `${project} go.mod is required`);
  assert.ok(existsSync(join(projectRoot, 'cmd', `juhe-ai-${project}`, 'main.go')), `${project} command is required`);
  assert.match(workspace, new RegExp(`\\./projects/${project}\\b`), `${project} must be in go.work`);
  const mod = read(join(projectRoot, 'go.mod'));
  assert.match(mod, /backend-go-contracts/, `${project} must use shared contracts`);
  for (const file of goFiles(projectRoot)) {
    const source = read(file);
    for (const other of projects) {
      if (other === project) continue;
      assert.doesNotMatch(source, new RegExp(`backend-go-${other.replace('-', '\\-')}`), `${relative(repoRoot, file)} imports ${other}`);
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
