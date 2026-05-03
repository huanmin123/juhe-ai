param(
  [string]$OutputDir = 'release',
  [string]$PackageName = 'juhe-ai-linux',
  [string]$FrontendApiBaseUrl = '/api',
  [string]$FrontendGatewayBaseUrl = '',
  [switch]$IncludeLocalEnv
)

$ErrorActionPreference = 'Stop'

$arguments = @(
  '-NoLogo',
  '-File',
  (Join-Path $PSScriptRoot 'package-release.ps1'),
  '-OutputDir',
  $OutputDir,
  '-PackageName',
  $PackageName,
  '-ArchiveFormat',
  'tar.gz',
  '-FrontendApiBaseUrl',
  $FrontendApiBaseUrl,
  '-FrontendGatewayBaseUrl',
  $FrontendGatewayBaseUrl
)

if ($IncludeLocalEnv) {
  $arguments += '-IncludeLocalEnv'
}

& pwsh @arguments
exit $LASTEXITCODE
