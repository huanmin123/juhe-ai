$ErrorActionPreference = 'Stop'

$root = Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..')
$rgCommand = Get-Command rg -ErrorAction Stop | Select-Object -First 1
$rg = $rgCommand.Source

$checks = @(
  @{
    Name = 'Go page-data runtime'
    Pattern = 'PageDataChange|pageDataChange|page_data_change|PageDataPublisher|pageDataPublisher|PublishPageData|PublishAccountsStatic|accountpagedata|announcementPageData'
    Paths = @('backend-go/internal')
    Args = @('--glob', '!store/postgres/postgresqueries/models.go')
  },
  @{
    Name = 'Go confirm route and config'
    Pattern = 'ManagementPageDataConfirmHandler|NewPageDataChangeConfirmHandler|data-changes/confirm|JUHE_AI_PAGE_DATA'
    Paths = @('backend-go/internal', 'backend-go/README.md')
    Args = @('--glob', '!**/*_test.go')
  },
  @{
    Name = 'current Go owner claim'
    Pattern = '页面数据确认接口 Go opt-in|Go management API 已新增.*data-changes/confirm|Go Redis adapter.*分阶段补齐'
    Paths = @('docs/architecture/backend/README.md', 'docs/migration', 'docs/plans/计划-0081-Node转Go渐进减法迁移.md')
    Args = @()
  },
  @{ Name = 'confirm route'; Pattern = 'data-changes/confirm'; Paths = @('frontend/src', 'backend/src', 'backend-go/internal', 'frontend/package.json', 'backend/package.json'); Args = @('--glob', '!**/*_test.go') },
  @{ Name = 'frontend page-data runtime'; Pattern = 'pageDataApi|PageDataRevisionToken|PageDataActivation|pageDataActivation|pageDataResourceCache|pageDataMutationInvalidation|pageDataGenerationFences|currentPageDataSecurityGeneration|advancePageData'; Paths = @('frontend/src'); Args = @() },
  @{ Name = 'Node page-data runtime'; Pattern = 'page-data|PageData|publishAccountStaticChange|publishAccountRuntimeChange|publishPageDataDomain|publishStatsPageData'; Paths = @('backend/src'); Args = @() },
  @{ Name = 'Go page-data runtime'; Pattern = 'page-data|PageData|pageData|page_data'; Paths = @('backend-go/internal'); Args = @('--glob', '!**/*_test.go') },
  @{ Name = 'dirty-domain current artifacts'; Pattern = 'page_data_dirty_domains|JuheBusinessPageDataDirtyDomain'; Paths = @('backend-go/internal', 'backend-go/db/queries'); Args = @('--glob', '!**/*_test.go') },
  @{ Name = 'retired scripts'; Pattern = 'test:page-data|smoke:page-data|benchmark:page-data'; Paths = @('frontend/package.json', 'backend/package.json'); Args = @() }
)

$removedPaths = @(
  'backend-go/internal/app/page_data_change.go',
  'backend-go/internal/app/page_data_change_recovery.go',
  'backend-go/internal/httpapi/page_data_change_confirm.go',
  'backend-go/internal/modules/accountpagedata',
  'backend-go/internal/platform/redis/page_data_change.go',
  'backend-go/internal/store/port/page_data_dirty_domains.go',
  'backend-go/internal/store/postgres/page_data_dirty_domains.go',
  'backend-go/internal/store/postgres/queries/w7_page_data_dirty_domains.sql'
)

$failures = @()
foreach ($check in $checks) {
  $existingPaths = @($check.Paths | ForEach-Object { Join-Path $root $_ } | Where-Object { Test-Path -LiteralPath $_ })
  if ($existingPaths.Count -eq 0) { continue }
  $output = @(& $rg -n --no-heading --color never @($check.Args) $check.Pattern @existingPaths 2>$null)
  $exit = $LASTEXITCODE
  if ($exit -eq 0) {
    $failures += "[$($check.Name)]`n$($output -join "`n")"
  } elseif ($exit -ne 1) {
    throw "rg failed for $($check.Name): $exit"
  }
}

foreach ($path in $removedPaths) {
  $absolutePath = Join-Path $root $path
  if (Test-Path -LiteralPath $absolutePath) {
    $failures += "[removed Go path still exists]`n$absolutePath"
  }
}

if ($failures.Count -gt 0) {
  Write-Error ("Go page-data removal gate failed:`n" + ($failures -join "`n`n"))
}

Write-Output 'Go page-data removal gate passed'
