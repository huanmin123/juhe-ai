$ErrorActionPreference = 'Stop'

$root = Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..')
$rgCommand = Get-Command rg -ErrorAction Stop | Select-Object -First 1
$rg = $rgCommand.Source

$checks = @(
  @{ Name = 'confirm route'; Pattern = 'data-changes/confirm'; Paths = @('frontend/src', 'frontend/scripts', 'backend/src', 'backend-go/internal', 'frontend/package.json', 'backend/package.json') },
  @{ Name = 'frontend page-data runtime'; Pattern = 'pageDataApi|PageDataRevisionToken|PageDataActivation|pageDataActivation|pageDataResourceCache|getDefaultPageDataResourceCache|pageDataMutationInvalidation|pageDataGenerationFences|currentPageDataSecurityGeneration|advancePageData|accountTestOptionsCache|invalidateAccountTestOptionsCache|loadAccountTestOptionsCached'; Paths = @('frontend/src', 'frontend/scripts') },
  @{ Name = 'Node page-data runtime'; Pattern = 'page-data|PageData|publishAccountStaticChange|publishAccountRuntimeChange|publishPageDataDomain|publishStatsPageData'; Paths = @('backend/src') },
  @{ Name = 'Go page-data runtime'; Pattern = 'page-data|PageData|pageData|page_data'; Paths = @('backend-go/internal') },
  @{ Name = 'dirty-domain current artifacts'; Pattern = 'page_data_dirty_domains|JuheBusinessPageDataDirtyDomain'; Paths = @('backend-go/internal', 'backend-go/db/queries') },
  @{ Name = 'retired scripts'; Pattern = 'test:page-data|smoke:page-data|benchmark:page-data'; Paths = @('frontend/package.json', 'backend/package.json') }
)

$failures = @()
foreach ($check in $checks) {
  $existingPaths = @($check.Paths | ForEach-Object { Join-Path $root $_ } | Where-Object { Test-Path -LiteralPath $_ })
  if ($existingPaths.Count -eq 0) { continue }
  $output = @(& $rg -n --no-heading --color never $check.Pattern @existingPaths 2>$null)
  $exit = $LASTEXITCODE
  if ($exit -eq 0) {
    $failures += "[$($check.Name)]`n$($output -join "`n")"
  } elseif ($exit -ne 1) {
    throw "rg failed for $($check.Name): $exit"
  }
}

if ($failures.Count -gt 0) {
  Write-Error ("page-data removal gate failed:`n" + ($failures -join "`n`n"))
}

Write-Output 'page-data removal gate passed'
