import assert from 'node:assert/strict'
import { mkdtemp, mkdir, readFile, rm, symlink, writeFile } from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  ReleasePackageValidationError,
  validateReleasePackagePaths
} from './validate-release-package.mjs'
import { validateFrontendApiBase } from './frontend-api-base-contract.mjs'

const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'juhe-release-validator-'))

async function resetFixture() {
  const fixturePath = path.join(tempRoot, 'fixture')
  await rm(fixturePath, { force: true, recursive: true })
  await mkdir(fixturePath, { recursive: true })
  return fixturePath
}

async function expectRejected(relativePath, contents = 'blocked') {
  const fixturePath = await resetFixture()
  const targetPath = path.join(fixturePath, ...relativePath.split('/'))
  await mkdir(path.dirname(targetPath), { recursive: true })
  await writeFile(targetPath, contents)

  await assert.rejects(
    validateReleasePackagePaths([fixturePath]),
    ReleasePackageValidationError,
    relativePath
  )
}

try {
  for (const validApiBase of [
    '/__aisys__/api',
    'http://example.test/__aisys__/api',
    'https://example.test/C:/release/__aisys__/api',
    'https://example.test/a:/__aisys__/api',
    'https://example.test/a,b/__aisys__/api',
    'https://example.test/a;b/__aisys__/api',
    'https://example.test/a(b)/__aisys__/api'
  ]) {
    assert.equal(validateFrontendApiBase(validApiBase), validApiBase)
  }

  for (const invalidApiBase of [
    'C:/Program Files/__aisys__/api',
    '\\\\server\\release folder\\__aisys__\\api',
    '/Users/example/release folder/__aisys__/api',
    'file:///tmp(foo)/__aisys__/api',
    'http:\\\\example.test\\__aisys__\\api',
    '\\__aisys__\\api',
    '../__aisys__/api',
    'foo/__aisys__/api',
    'javascript:alert(1)/__aisys__/api',
    'https://example.test/release folder/__aisys__/api',
    'https://example.test/%ZZ/__aisys__/api'
  ]) {
    assert.throws(() => validateFrontendApiBase(invalidApiBase), undefined, invalidApiBase)
  }

  const validFixture = await resetFixture()
  await mkdir(path.join(validFixture, 'backend', 'dist'), { recursive: true })
  await mkdir(path.join(validFixture, 'frontend', 'dist', 'assets'), { recursive: true })
  await mkdir(path.join(validFixture, 'docs', 'deploy'), { recursive: true })
  await writeFile(path.join(validFixture, 'backend', '.env.example'), 'PORT=3001\n')
  await writeFile(path.join(validFixture, 'backend', 'dist', 'server.js'), 'export {}\n')
  await writeFile(
    path.join(validFixture, 'frontend', 'dist', 'assets', 'index.js'),
    [
      'const apiBase = "/__aisys__/api"',
      'const httpApiBase = "http://example.test/__aisys__/api"',
      'const httpsApiBase = "https://example.test/__aisys__/api"',
      'const httpsPathWithDriveToken = "https://example.test/C:/release/__aisys__/api"',
      'const httpsPathWithLowerDriveToken = "https://example.test/a:/__aisys__/api"',
      'const httpsPathWithComma = "https://example.test/a,b/__aisys__/api"',
      'const httpsPathWithSemicolon = "https://example.test/a;b/__aisys__/api"',
      'const httpsPathWithParentheses = "https://example.test/a(b)/__aisys__/api"',
      'const apiDocumentation = "保护 /__aisys__/api 后台接口，避免压垮 DB service。"',
      'const apiEndpoint = fetch("/__aisys__/api/auth/me")'
    ].join('\n') + '\n'
  )
  await writeFile(
    path.join(validFixture, 'frontend', 'dist', 'api-keys.html'),
    '<a href="/__aisys__/api-keys">API Key</a>\n'
  )
  await writeFile(path.join(validFixture, 'docs', 'deploy', 'migration.sql'), 'select 1;\n')
  await validateReleasePackagePaths([validFixture])
  await validateReleasePackagePaths([path.join(validFixture, 'frontend', 'dist')])

  const invalidDirectFrontendFixture = await resetFixture()
  const invalidDirectFrontendDist = path.join(invalidDirectFrontendFixture, 'frontend', 'dist')
  await mkdir(path.join(invalidDirectFrontendDist, 'assets'), { recursive: true })
  await writeFile(
    path.join(invalidDirectFrontendDist, 'assets', 'index.js'),
    'const apiBase = "E:/Git/__aisys__/api"\n'
  )
  await assert.rejects(
    validateReleasePackagePaths([invalidDirectFrontendDist]),
    /frontend API base contains a Windows drive path/u,
    'direct frontend/dist validation must reject an invalid API base'
  )

  const missingDirectFrontendFixture = await resetFixture()
  const missingDirectFrontendDist = path.join(missingDirectFrontendFixture, 'frontend', 'dist')
  await mkdir(path.join(missingDirectFrontendDist, 'assets'), { recursive: true })
  await writeFile(path.join(missingDirectFrontendDist, 'assets', 'index.js'), 'const apiBase = "/api"\n')
  await assert.rejects(
    validateReleasePackagePaths([missingDirectFrontendDist]),
    /frontend runtime bundle does not contain the required API marker/u,
    'direct frontend/dist validation must require the runtime API marker'
  )

  for (const { value: invalidApiBase, reason } of [
    { value: 'E:/Git/__aisys__/api', reason: 'frontend API base contains a Windows drive path' },
    { value: 'E:\\\\Git\\\\__aisys__\\\\api', reason: 'frontend API base contains a Windows drive path' },
    { value: '\\\\server\\share\\__aisys__\\api', reason: 'frontend API base contains a UNC path' },
    { value: '//example.test/__aisys__/api', reason: 'frontend API base must not be protocol-relative' },
    { value: 'file://example.test/__aisys__/api', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'file:/tmp/__aisys__/api', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'http:/example.test/__aisys__/api', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'http:///__aisys__/api', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'http://?next=/__aisys__/api', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'https://example.test/__aisys__/api?x=1', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'https://example.test/__aisys__/api#x', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'https://example.test/__aisys__/api/extra', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'https://example.test/__aisys__/api ', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'https://example.test/__aisys__/api\\', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'https://example.test/__aisys__/api%ZZ', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'https://example.test/__aisys__/api%2Fextra', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'https://example.test/release /__aisys__/api', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'file:///tmp /__aisys__/api', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'C:/Program Files /__aisys__/api', reason: 'frontend API base contains a Windows drive path' },
    { value: 'https://example.test/__aisys__/api-keys', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'https://example.test/__aisys__/api2', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'https://example.test/__aisys__/api_extra', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'HTTPS://example.test/__aisys__/api', reason: 'frontend API base contains an invalid absolute URL' },
    { value: 'https://example.test/a/../__aisys__/api', reason: 'frontend API base contains an invalid absolute URL' },
    { value: '/Users/example/release/__aisys__/api', reason: 'frontend API base contains a filesystem path' }
  ]) {
    const invalidBundleFixture = await resetFixture()
    const bundlePath = path.join(invalidBundleFixture, 'frontend', 'dist', 'assets', 'index.js')
    await mkdir(path.dirname(bundlePath), { recursive: true })
    await writeFile(
      bundlePath,
      [
        `const apiBase = ${JSON.stringify(invalidApiBase)}`,
        'const fallbackApiBase = "/__aisys__/api"'
      ].join('\n') + '\n'
    )
    await assert.rejects(
      validateReleasePackagePaths([invalidBundleFixture]),
      {
        name: 'ReleasePackageValidationError',
        message: new RegExp(reason.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'u')
      },
      `expected ${invalidApiBase} to be rejected with ${reason}`
    )
  }

  for (const invalidHtmlApiBase of [
    '/__aisys__/api;evil',
    '/__aisys__/api,evil',
    '/__aisys__/api)evil'
  ]) {
    const invalidHtmlFixture = await resetFixture()
    const validBundlePath = path.join(invalidHtmlFixture, 'frontend', 'dist', 'assets', 'index.js')
    const invalidHtmlPath = path.join(invalidHtmlFixture, 'frontend', 'dist', 'index.html')
    await mkdir(path.dirname(validBundlePath), { recursive: true })
    await writeFile(validBundlePath, 'const apiBase = "/__aisys__/api"\n')
    await writeFile(invalidHtmlPath, `<meta data-api=${invalidHtmlApiBase}>\n`)
    await assert.rejects(
      validateReleasePackagePaths([invalidHtmlFixture]),
      ReleasePackageValidationError,
      `expected unquoted HTML API base ${invalidHtmlApiBase} to be rejected`
    )
  }

  const longPathSegment = 'x'.repeat(4096)
  for (const { value: invalidApiBase, reason } of [
    { value: `E:/${longPathSegment}/__aisys__/api`, reason: 'frontend API base contains a Windows drive path' },
    { value: `//example.test/${longPathSegment}/__aisys__/api`, reason: 'frontend API base must not be protocol-relative' },
    { value: `/Users/example/${longPathSegment}/__aisys__/api`, reason: 'frontend API base contains a filesystem path' }
  ]) {
    const invalidLongBundleFixture = await resetFixture()
    const bundlePath = path.join(invalidLongBundleFixture, 'frontend', 'dist', 'assets', 'index.js')
    await mkdir(path.dirname(bundlePath), { recursive: true })
    await writeFile(
      bundlePath,
      `const apiBase = ${JSON.stringify(invalidApiBase)}\nconst fallbackApiBase = "/__aisys__/api"\n`
    )
    await assert.rejects(
      validateReleasePackagePaths([invalidLongBundleFixture]),
      new RegExp(reason.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'u')
    )
  }

  const missingApiBaseFixture = await resetFixture()
  const missingApiBundlePath = path.join(missingApiBaseFixture, 'frontend', 'dist', 'assets', 'index.js')
  await mkdir(path.dirname(missingApiBundlePath), { recursive: true })
  await writeFile(missingApiBundlePath, 'const apiBase = "/api"\n')
  await writeFile(
    path.join(missingApiBaseFixture, 'frontend', 'dist', 'help.html'),
    '<p>API reference: /__aisys__/api</p>\n'
  )
  await assert.rejects(
    validateReleasePackagePaths([missingApiBaseFixture]),
    /frontend runtime bundle does not contain the required API marker/u,
    'non-runtime help content must not satisfy the frontend asset marker gate'
  )

  const commentOnlyMarkerFixture = await resetFixture()
  const commentOnlyBundlePath = path.join(
    commentOnlyMarkerFixture,
    'frontend',
    'dist',
    'assets',
    'index.js'
  )
  await mkdir(path.dirname(commentOnlyBundlePath), { recursive: true })
  await writeFile(
    commentOnlyBundlePath,
    [
      '// runtime API base: /__aisys__/api',
      '/* fallback marker /__aisys__/api */',
      'fetch("/__aisys__/api/auth/me")'
    ].join('\n') + '\n'
  )
  await assert.rejects(
    validateReleasePackagePaths([commentOnlyMarkerFixture]),
    /frontend runtime bundle does not contain the required API marker/u,
    'comments and endpoint references must not satisfy the frontend asset marker gate'
  )

  for (const multilineApiBase of [
    'https://example.test/release\n/__aisys__/api',
    '/__aisys__/api\n-keys'
  ]) {
    const multilineFixture = await resetFixture()
    const multilineBundlePath = path.join(multilineFixture, 'frontend', 'dist', 'assets', 'index.js')
    await mkdir(path.dirname(multilineBundlePath), { recursive: true })
    await writeFile(
      multilineBundlePath,
      [
        `const apiBase = \`${multilineApiBase}\``,
        'const fallbackApiBase = "/__aisys__/api"'
      ].join('\n') + '\n'
    )
    await assert.rejects(
      validateReleasePackagePaths([multilineFixture]),
      ReleasePackageValidationError,
      `expected multiline API base ${JSON.stringify(multilineApiBase)} to be rejected`
    )
  }

  for (const dynamicTemplate of [
    'const apiBase = `https://${host}/__aisys__/api`'
  ]) {
    const dynamicTemplateFixture = await resetFixture()
    const dynamicTemplateBundlePath = path.join(
      dynamicTemplateFixture,
      'frontend',
      'dist',
      'assets',
      'index.js'
    )
    await mkdir(path.dirname(dynamicTemplateBundlePath), { recursive: true })
    await writeFile(
      dynamicTemplateBundlePath,
      `${dynamicTemplate}\nconst fallbackApiBase = "/__aisys__/api"\n`
    )
    await assert.rejects(
      validateReleasePackagePaths([dynamicTemplateFixture]),
      /frontend API base must not be assembled by a dynamic template expression/u,
      `expected dynamic API base ${JSON.stringify(dynamicTemplate)} to be rejected`
    )
  }

  for (const staticExpression of [
    'const apiBase = "/__aisys__" + "/api"',
    'const apiBase = `/__aisys__/api`'
  ]) {
    const staticExpressionFixture = await resetFixture()
    const staticExpressionBundlePath = path.join(
      staticExpressionFixture,
      'frontend',
      'dist',
      'assets',
      'index.js'
    )
    await mkdir(path.dirname(staticExpressionBundlePath), { recursive: true })
    await writeFile(
      staticExpressionBundlePath,
      `${staticExpression}\n`
    )
    await validateReleasePackagePaths([staticExpressionFixture])
  }

  const invalidStaticConcatFixture = await resetFixture()
  const invalidStaticConcatBundlePath = path.join(
    invalidStaticConcatFixture,
    'frontend',
    'dist',
    'assets',
    'index.js'
  )
  await mkdir(path.dirname(invalidStaticConcatBundlePath), { recursive: true })
  await writeFile(
    invalidStaticConcatBundlePath,
    'const apiBase = "E:/Git" + "/__aisys__/api"\n'
  )
  await assert.rejects(
    validateReleasePackagePaths([invalidStaticConcatFixture]),
    /frontend API base contains a Windows drive path/u,
    'static string concatenation must be validated after AST evaluation'
  )

  const escapedMarkerFixture = await resetFixture()
  const escapedMarkerBundlePath = path.join(
    escapedMarkerFixture,
    'frontend',
    'dist',
    'assets',
    'index.js'
  )
  await mkdir(path.dirname(escapedMarkerBundlePath), { recursive: true })
  await writeFile(
    escapedMarkerBundlePath,
    String.raw`const apiBase = "/\u005f\u005faisys__/api"` + '\n'
  )
  await assert.rejects(
    validateReleasePackagePaths([escapedMarkerFixture]),
    /must not hide the API marker behind a Unicode or hexadecimal escape/u,
    'Unicode-escaped API markers must fail closed'
  )

  const packageReleaseShell = await readFile(
    path.join(path.dirname(fileURLToPath(import.meta.url)), 'package-release.sh'),
    'utf8'
  )
  assert.match(
    packageReleaseShell,
    /FRONTEND_API_BASE_URL="\$\{JUHE_AI_FRONTEND_API_BASE_URL:-\/__aisys__\/api\}"/u,
    'Git Bash/MSYS packaging must expose an explicit environment-variable API base entry'
  )
  assert.match(
    packageReleaseShell,
    /export MSYS2_ARG_CONV_EXCL=.*\$\{FRONTEND_API_BASE_URL\}/u,
    'Git Bash/MSYS packaging must protect the validated API base command argument'
  )
  assert.match(
    packageReleaseShell,
    /export MSYS2_ENV_CONV_EXCL=.*VITE_JUHE_AI_API_BASE_URL/u,
    'Git Bash/MSYS packaging must protect the frontend build environment value'
  )

  const frontendTextPaths = [
    'frontend/dist/runtime-config.json',
    'frontend/dist/runtime-config.txt',
    'frontend/dist/runtime-config.webmanifest',
    'frontend/dist/runtime-config.xml',
    'frontend/dist/assets/runtime-config.css',
    'frontend/dist/assets/runtime-config.html',
    'frontend/dist/assets/runtime-config.js',
    'frontend/dist/assets/runtime-config.mjs',
    'frontend/dist/assets/runtime-config.svg',
    'frontend/dist/assets/index.js.map'
  ]

  const crossFormatInvalidApiBases = [
    'C:/Program Files/__aisys__/api',
    '\\\\server\\release folder\\__aisys__\\api',
    '/Users/example/release folder/__aisys__/api',
    'file:///tmp(foo)/__aisys__/api',
    'http:\\\\example.test\\__aisys__\\api',
    '\\__aisys__\\api',
    '../__aisys__/api',
    'foo/__aisys__/api',
    'javascript:alert(1)/__aisys__/api',
    'https://example.test/__aisys__/api?x=1',
    'https://example.test/__aisys__/api#x',
    'https://example.test/__aisys__/api/extra',
    'https://example.test/__aisys__/api\\',
    'https://example.test/__aisys__/api%ZZ',
    'https://example.test/__aisys__/api%2Fextra'
  ]

  for (const relativePath of frontendTextPaths) {
    for (const invalidApiBase of crossFormatInvalidApiBases) {
      const invalidCrossFormatFixture = await resetFixture()
      const validBundlePath = path.join(invalidCrossFormatFixture, 'frontend', 'dist', 'assets', 'index.js')
      const invalidTextPath = path.join(invalidCrossFormatFixture, ...relativePath.split('/'))
      await mkdir(path.dirname(validBundlePath), { recursive: true })
      await mkdir(path.dirname(invalidTextPath), { recursive: true })
      await writeFile(validBundlePath, 'const apiBase = "/__aisys__/api"\n')
      const invalidExtension = path.extname(invalidTextPath).toLowerCase()
      const invalidContents = invalidExtension === '.js' || invalidExtension === '.mjs'
        ? `const apiBase = ${JSON.stringify(invalidApiBase)}\n`
        : `${invalidApiBase}\n`
      await writeFile(invalidTextPath, invalidContents)
      await assert.rejects(
        validateReleasePackagePaths([invalidCrossFormatFixture]),
        ReleasePackageValidationError,
        `${relativePath}: ${invalidApiBase}`
      )
    }
  }

  for (const relativePath of frontendTextPaths) {
    const invalidTextFixture = await resetFixture()
    const validBundlePath = path.join(invalidTextFixture, 'frontend', 'dist', 'assets', 'index.js')
    const invalidTextPath = path.join(invalidTextFixture, ...relativePath.split('/'))
    await mkdir(path.dirname(validBundlePath), { recursive: true })
    await mkdir(path.dirname(invalidTextPath), { recursive: true })
    await writeFile(validBundlePath, 'const apiBase = "/__aisys__/api"\n')
    const extension = path.extname(relativePath)
    const invalidContents = extension === '.js' || extension === '.mjs'
      ? 'const apiBase = "/Users/example/release/__aisys__/api"\n'
      : extension === '.css'
        ? 'body{background:url(/Users/example/release/__aisys__/api)}\n'
        : extension === '.html'
          ? '<meta data-api=/Users/example/release/__aisys__/api>\n'
          : '/Users/example/release/__aisys__/api\n'
    await writeFile(invalidTextPath, invalidContents)
    await assert.rejects(
      validateReleasePackagePaths([invalidTextFixture]),
      /frontend API base contains a filesystem path/u,
      `expected invalid API base in ${relativePath} to be rejected`
    )
  }

  for (const relativePath of frontendTextPaths) {
    const invalidLongTextFixture = await resetFixture()
    const validBundlePath = path.join(invalidLongTextFixture, 'frontend', 'dist', 'assets', 'index.js')
    const invalidTextPath = path.join(invalidLongTextFixture, ...relativePath.split('/'))
    await mkdir(path.dirname(validBundlePath), { recursive: true })
    await mkdir(path.dirname(invalidTextPath), { recursive: true })
    await writeFile(validBundlePath, 'const apiBase = "/__aisys__/api"\n')
    const extension = path.extname(relativePath)
    const invalidLongValue = `/Users/example/${longPathSegment}/__aisys__/api`
    await writeFile(
      invalidTextPath,
      extension === '.js' || extension === '.mjs'
        ? `const apiBase = ${JSON.stringify(invalidLongValue)}\n`
        : `${invalidLongValue}\n`
    )
    await assert.rejects(
      validateReleasePackagePaths([invalidLongTextFixture]),
      /frontend API base contains a filesystem path/u,
      `expected long invalid API base in ${relativePath} to be rejected`
    )
  }

  for (const forbiddenPath of [
    'backend/data/state.json',
    'backend/logs/server.txt',
    'backend/node_modules/pkg/index.js',
    'backend/.env',
    'backend/.env.production',
    'backend/.env.example.local',
    'backend/app.log',
    'backend/state.sqlite',
    'backend/state.sqlite3',
    'backend/state.db',
    'backend/state.db3',
    'backend/backup.dump',
    'backend/dump.rdb',
    'backend/appendonly.aof.1.incr.aof',
    'backend/state.db-wal',
    'backend/state.db-shm',
    'backend/state.db-journal'
  ]) {
    await expectRejected(forbiddenPath)
  }

  const linksOnlyFixture = await resetFixture()
  await mkdir(path.join(linksOnlyFixture, 'data'), { recursive: true })
  await writeFile(path.join(linksOnlyFixture, 'data', 'allowed-during-source-scan.txt'), 'ok')
  await validateReleasePackagePaths([linksOnlyFixture], { linksOnly: true })

  const linkFixture = await resetFixture()
  const linkTarget = path.join(tempRoot, 'link-target')
  await mkdir(linkTarget, { recursive: true })
  await symlink(
    linkTarget,
    path.join(linkFixture, 'linked-directory'),
    process.platform === 'win32' ? 'junction' : 'dir'
  )
  await assert.rejects(
    validateReleasePackagePaths([linkFixture]),
    /symbolic links and junctions are forbidden/u
  )

  process.stdout.write('Release package validator tests passed.\n')
} finally {
  await rm(tempRoot, { force: true, recursive: true })
}
