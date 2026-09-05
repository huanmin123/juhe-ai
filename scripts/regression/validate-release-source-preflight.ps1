$ErrorActionPreference = 'Stop'

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..\..')
$preflightScript = Join-Path $repoRoot 'scripts\assert-release-source.ps1'
$bashPreflightScript = (Join-Path $repoRoot 'scripts\assert-release-source.sh') -replace '\\', '/'
$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("juhe-ai-release-source-{0}" -f [guid]::NewGuid().ToString('N'))
$resolvedTempBase = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())

function Invoke-Preflight {
  param(
    [Parameter(Mandatory = $true)][string]$WorkingTree,
    [Parameter(Mandatory = $true)][string]$ExpectedCommit
  )

  try {
    $output = @(& $preflightScript -RepoRoot $WorkingTree -ExpectedCommit $ExpectedCommit 2>&1)
    return [pscustomobject]@{
      ExitCode = 0
      Output = ($output -join "`n")
    }
  } catch {
    return [pscustomobject]@{
      ExitCode = 1
      Output = $_.ToString()
    }
  }
}

function Invoke-BashPreflight {
  param(
    [Parameter(Mandatory = $true)][string]$WorkingTree,
    [Parameter(Mandatory = $true)][string]$ExpectedCommit
  )

  $bashWorkingTree = $WorkingTree -replace '\\', '/'
  $output = & bash $bashPreflightScript $bashWorkingTree $ExpectedCommit 2>&1
  return [pscustomobject]@{
    ExitCode = $LASTEXITCODE
    Output = ($output -join "`n")
  }
}

try {
  New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
  git -C $tempRoot init --quiet
  git -C $tempRoot config user.email 'release-preflight@example.invalid'
  git -C $tempRoot config user.name 'Release Preflight Test'
  Set-Content -LiteralPath (Join-Path $tempRoot 'tracked.txt') -Value 'baseline' -NoNewline
  git -C $tempRoot add tracked.txt
  git -C $tempRoot commit --quiet -m 'baseline'
  $commit = (git -C $tempRoot rev-parse HEAD).Trim()

  $clean = Invoke-Preflight -WorkingTree $tempRoot -ExpectedCommit $commit
  if ($clean.ExitCode -ne 0 -or $clean.Output -notmatch 'RELEASE_SOURCE_OK') {
    throw "clean source should pass: $($clean.Output)"
  }
  if (-not $IsWindows) {
    $cleanBash = Invoke-BashPreflight -WorkingTree $tempRoot -ExpectedCommit $commit
    if ($cleanBash.ExitCode -ne 0 -or $cleanBash.Output -notmatch 'RELEASE_SOURCE_OK') {
      throw "clean source should pass bash preflight: $($cleanBash.Output)"
    }
  }

  Set-Content -LiteralPath (Join-Path $tempRoot 'tracked.txt') -Value 'dirty' -NoNewline
  $dirtyTracked = Invoke-Preflight -WorkingTree $tempRoot -ExpectedCommit $commit
  if ($dirtyTracked.ExitCode -eq 0 -or $dirtyTracked.Output -notmatch 'not clean') {
    throw "dirty tracked source should fail: $($dirtyTracked.Output)"
  }
  if (-not $IsWindows) {
    $dirtyTrackedBash = Invoke-BashPreflight -WorkingTree $tempRoot -ExpectedCommit $commit
    if ($dirtyTrackedBash.ExitCode -eq 0 -or $dirtyTrackedBash.Output -notmatch 'not clean') {
      throw "dirty tracked source should fail bash preflight: $($dirtyTrackedBash.Output)"
    }
  }

  Set-Content -LiteralPath (Join-Path $tempRoot 'tracked.txt') -Value 'baseline' -NoNewline
  Set-Content -LiteralPath (Join-Path $tempRoot 'untracked.txt') -Value 'untracked' -NoNewline
  $dirtyUntracked = Invoke-Preflight -WorkingTree $tempRoot -ExpectedCommit $commit
  if ($dirtyUntracked.ExitCode -eq 0 -or $dirtyUntracked.Output -notmatch 'not clean') {
    throw "untracked source should fail: $($dirtyUntracked.Output)"
  }
  if (-not $IsWindows) {
    $dirtyUntrackedBash = Invoke-BashPreflight -WorkingTree $tempRoot -ExpectedCommit $commit
    if ($dirtyUntrackedBash.ExitCode -eq 0 -or $dirtyUntrackedBash.Output -notmatch 'not clean') {
      throw "untracked source should fail bash preflight: $($dirtyUntrackedBash.Output)"
    }
  }

  Remove-Item -LiteralPath (Join-Path $tempRoot 'untracked.txt') -Force
  $wrongCommit = ('0' * 40)
  $mismatch = Invoke-Preflight -WorkingTree $tempRoot -ExpectedCommit $wrongCommit
  if ($mismatch.ExitCode -eq 0 -or $mismatch.Output -notmatch 'does not match expected commit') {
    throw "commit mismatch should fail: $($mismatch.Output)"
  }
  if (-not $IsWindows) {
    $mismatchBash = Invoke-BashPreflight -WorkingTree $tempRoot -ExpectedCommit $wrongCommit
    if ($mismatchBash.ExitCode -eq 0 -or $mismatchBash.Output -notmatch 'does not match expected commit') {
      throw "commit mismatch should fail bash preflight: $($mismatchBash.Output)"
    }
  }

  $packagePowerShellPath = Join-Path $repoRoot 'scripts\package-release.ps1'
  $contractPath = Join-Path $repoRoot 'scripts\frontend-api-base-contract.mjs'
  $invalidPowerShellOutput = try {
    & $packagePowerShellPath -OutputDir (Join-Path $tempRoot 'invalid-powershell-output') -FrontendApiBaseUrl 'E:/Git/__aisys__/api' 2>&1 | Out-String
    throw 'PowerShell package script accepted a filesystem frontend API base URL'
  } catch {
    $_.ToString()
  }
  if ($invalidPowerShellOutput -notmatch 'FrontendApiBaseUrl is invalid') {
    throw "PowerShell package script returned the wrong API base error: $invalidPowerShellOutput"
  }
  if (Test-Path -LiteralPath (Join-Path $tempRoot 'invalid-powershell-output')) {
    throw 'PowerShell package script mutated output before rejecting the API base URL'
  }

  $invalidUnixPowerShellPath = Join-Path $tempRoot 'invalid-powershell-unix-output'
  $invalidUnixPowerShellOutput = try {
    & $packagePowerShellPath -OutputDir $invalidUnixPowerShellPath -FrontendApiBaseUrl '/Users/example/release/__aisys__/api' 2>&1 | Out-String
    throw 'PowerShell package script accepted a Unix filesystem frontend API base URL'
  } catch {
    $_.ToString()
  }
  if ($invalidUnixPowerShellOutput -notmatch 'FrontendApiBaseUrl is invalid') {
    throw "PowerShell package script returned the wrong Unix API base error: $invalidUnixPowerShellOutput"
  }
  if (Test-Path -LiteralPath $invalidUnixPowerShellPath) {
    throw 'PowerShell package script mutated output before rejecting the Unix API base URL'
  }

  $pathAtLimit = "https://example.test/$((('x' * 1009) -join ''))/__aisys__/api"
  $pathOverLimit = "https://example.test/$((('x' * 1010) -join ''))/__aisys__/api"
  $hostAtTotalLimit = ((('a.' * 1010) -join '') + 'comx')
  $totalAtLimit = "https://$hostAtTotalLimit/x/__aisys__/api"
  $totalOverLimit = "${totalAtLimit}x"
  foreach ($boundaryValue in @($pathAtLimit, $totalAtLimit)) {
    $boundaryOutput = @(& node $contractPath $boundaryValue 2>&1)
    if ($LASTEXITCODE -ne 0) {
      throw "shared API base contract rejected a boundary value that should pass: $($boundaryOutput -join "`n")"
    }
  }

  $invalidApiBases = @(
    'http:///__aisys__/api',
    'https:////example.test/__aisys__/api',
    'https://example.test/__aisys__/api?',
    'https://example.test/__aisys__/api#',
    'https://user@example.test/__aisys__/api',
    'https://example.test:/__aisys__/api',
    'https://example.test:invalid/__aisys__/api',
    'https://example.test:65536/__aisys__/api',
    'https://%65xample.test/__aisys__/api',
    'https://example.test/release path/__aisys__/api',
    'https://example.test/%ZZ/__aisys__/api',
    'HTTPS://example.test/__aisys__/api',
    'https://example.test/a/../__aisys__/api',
    $pathOverLimit,
    $totalOverLimit
  )
  foreach ($invalidApiBase in $invalidApiBases) {
    $invalidOutputName = 'invalid-powershell-contract-' + ([Guid]::NewGuid().ToString('N'))
    $invalidOutputPath = Join-Path $tempRoot $invalidOutputName
    $invalidOutput = try {
      & $packagePowerShellPath -OutputDir $invalidOutputPath -FrontendApiBaseUrl $invalidApiBase 2>&1 | Out-String
      throw "PowerShell package script accepted invalid API base: $invalidApiBase"
    } catch {
      $_.ToString()
    }
    if ($invalidOutput -notmatch 'FrontendApiBaseUrl is invalid') {
      throw "PowerShell package script returned the wrong API base error for ${invalidApiBase}: $invalidOutput"
    }
    if (Test-Path -LiteralPath $invalidOutputPath) {
      throw "PowerShell package script mutated output before rejecting invalid API base: $invalidApiBase"
    }
  }

  $bashCommands = @(Get-Command bash -All -ErrorAction SilentlyContinue | Where-Object Source | Sort-Object Source -Unique)
  if ($bashCommands.Count -gt 0) {
    $packageBashPath = (Join-Path $repoRoot 'scripts\package-release.sh') -replace '\\', '/'
    if ($IsWindows) {
      $originalOS = $env:OS
      try {
        foreach ($windowsBashCommand in $bashCommands) {
          $bashLabel = [IO.Path]::GetFileName((Split-Path -Parent $windowsBashCommand.Source))
          $windowsBashCases = @(
            [pscustomobject]@{ Name = 'normal'; OS = $originalOS; Args = @('--output-dir', ((Join-Path $tempRoot "unsupported-$bashLabel-normal") -replace '\\', '/')); OutputPath = (Join-Path $tempRoot "unsupported-$bashLabel-normal") },
            [pscustomobject]@{ Name = 'help'; OS = $originalOS; Args = @('--help'); OutputPath = $null },
            [pscustomobject]@{ Name = 'unknown'; OS = $originalOS; Args = @('--unknown-option'); OutputPath = $null },
            [pscustomobject]@{ Name = 'missing-value'; OS = $originalOS; Args = @('--output-dir'); OutputPath = $null },
            [pscustomobject]@{ Name = 'empty-os'; OS = ''; Args = @('--goos', 'darwin', '--goarch', 'amd64', '--output-dir', ((Join-Path $tempRoot "unsupported-$bashLabel-empty-os") -replace '\\', '/')); OutputPath = (Join-Path $tempRoot "unsupported-$bashLabel-empty-os") },
            [pscustomobject]@{ Name = 'overridden-os'; OS = 'NotWindows'; Args = @('--goos', 'darwin', '--goarch', 'amd64', '--output-dir', ((Join-Path $tempRoot "unsupported-$bashLabel-overridden-os") -replace '\\', '/')); OutputPath = (Join-Path $tempRoot "unsupported-$bashLabel-overridden-os") }
          )
          foreach ($windowsBashCase in $windowsBashCases) {
            $env:OS = $windowsBashCase.OS
            $unsupportedOutput = @(& $windowsBashCommand.Source $packageBashPath @($windowsBashCase.Args) 2>&1)
            if ($LASTEXITCODE -ne 2 -or ($unsupportedOutput -join "`n") -notmatch 'requires native macOS or Linux') {
              throw "Windows bash package entry must fail at the environment gate ($($windowsBashCommand.Source), $($windowsBashCase.Name)): $($unsupportedOutput -join "`n")"
            }
            if ($windowsBashCase.OutputPath -and (Test-Path -LiteralPath $windowsBashCase.OutputPath)) {
              throw "Windows bash package entry mutated output before rejecting the unsupported environment: $($windowsBashCommand.Source), $($windowsBashCase.Name)"
            }
          }
        }
      } finally {
        $env:OS = $originalOS
      }
    } else {
      $bashCommand = $bashCommands | Select-Object -First 1
      $invalidUnixBashPath = Join-Path $tempRoot 'invalid-bash-unix-output'
      $invalidUnixBashOutput = @(& $bashCommand.Source $packageBashPath --output-dir $invalidUnixBashPath --frontend-api-base-url '/Users/example/release/__aisys__/api' 2>&1)
      if ($LASTEXITCODE -eq 0 -or ($invalidUnixBashOutput -join "`n") -notmatch 'shared strict contract') {
        throw "native bash package script accepted a Unix filesystem frontend API base URL: $($invalidUnixBashOutput -join "`n")"
      }
      if (Test-Path -LiteralPath $invalidUnixBashPath) {
        throw 'native bash package script mutated output before rejecting the Unix API base URL'
      }
    }
  }

  $packagePowerShell = Get-Content -LiteralPath (Join-Path $repoRoot 'scripts\package-release.ps1') -Raw
  foreach ($required in @('ExpectedCommit', 'assert-release-source.ps1', 'RELEASE_SOURCE_COMMIT', 'Assert-SafeFrontendApiBaseUrl', 'frontend-api-base-contract.mjs', 'Invoke-ReleasePackageValidator -Paths @($packageRoot)', 'New-VerifiedTarGzArchive', '& tar -tzf $ArchivePath', 'tar.gz archive creation failed with exit code')) {
    if ($packagePowerShell -notmatch [regex]::Escape($required)) {
      throw "PowerShell package script must include release source gate: $required"
    }
  }
  if ([regex]::Matches($packagePowerShell, 'assert-release-source\.ps1').Count -lt 2) {
    throw 'PowerShell package script must recheck the release source after the build'
  }
  if ($packagePowerShell.IndexOf("Copy-RequiredItem (Join-Path `$repoRoot 'frontend/dist')", [StringComparison]::Ordinal) -gt $packagePowerShell.IndexOf('Invoke-ReleasePackageValidator -Paths @($packageRoot)', [StringComparison]::Ordinal)) {
    throw 'PowerShell package script must validate the completed frontend bundle before archive creation'
  }
  $parserTokens = $null
  $parserErrors = $null
  $packagePowerShellAst = [System.Management.Automation.Language.Parser]::ParseFile(
    (Join-Path $repoRoot 'scripts\package-release.ps1'),
    [ref]$parserTokens,
    [ref]$parserErrors
  )
  if ($parserErrors.Count -ne 0) {
    throw "PowerShell package script failed to parse: $($parserErrors -join '; ')"
  }
  $tarFunctionAst = $packagePowerShellAst.Find({
      param($node)
      $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'New-VerifiedTarGzArchive'
    }, $true)
  if ($null -eq $tarFunctionAst) {
    throw 'PowerShell package script must define New-VerifiedTarGzArchive'
  }
  Invoke-Expression $tarFunctionAst.Extent.Text
  $fakeTarDirectory = Join-Path $tempRoot 'fake-tar-bin'
  $fakeTarReleaseDirectory = Join-Path $tempRoot 'fake-tar-release'
  $fakeTarArchive = Join-Path $tempRoot 'fake-tar-output.tar.gz'
  New-Item -ItemType Directory -Path $fakeTarDirectory, (Join-Path $fakeTarReleaseDirectory 'package') -Force | Out-Null
  if ($IsWindows) {
    Set-Content -LiteralPath (Join-Path $fakeTarDirectory 'tar.cmd') -Value "@echo off`r`nexit /b 7`r`n" -NoNewline
  } else {
    $fakeTarPath = Join-Path $fakeTarDirectory 'tar'
    Set-Content -LiteralPath $fakeTarPath -Value "#!/bin/sh`nexit 7`n" -NoNewline
    & chmod +x $fakeTarPath
    if ($LASTEXITCODE -ne 0) {
      throw 'failed to make the fake tar command executable'
    }
  }
  $originalPath = $env:PATH
  try {
    $env:PATH = "$fakeTarDirectory$([System.IO.Path]::PathSeparator)$originalPath"
    $tarFailureOutput = try {
      New-VerifiedTarGzArchive -ArchivePath $fakeTarArchive -ReleaseDirectory $fakeTarReleaseDirectory -ReleasePackageName 'package' 2>&1 | Out-String
      throw 'PowerShell package script accepted a failed tar command'
    } catch {
      $_.ToString()
    }
  } finally {
    $env:PATH = $originalPath
  }
  if ($tarFailureOutput -notmatch 'tar\.gz archive creation failed with exit code 7') {
    throw "PowerShell package script returned the wrong tar failure: $tarFailureOutput"
  }
  if ($tarFailureOutput -match '==> Done:') {
    throw 'PowerShell package script reported completion after tar failed'
  }
  $packageBash = Get-Content -LiteralPath (Join-Path $repoRoot 'scripts\package-release.sh') -Raw
  foreach ($required in @('--expected-commit', 'assert-release-source.sh', 'RELEASE_SOURCE_COMMIT', 'frontend-api-base-contract.mjs', 'shared strict contract', 'Windows_NT:*', '*:Windows_NT', 'requires native macOS or Linux', '-buildvcs=false', 'node "$VALIDATOR_PATH" --quiet --deploy-mode=go "$PACKAGE_ROOT"')) {
    if ($packageBash -notmatch [regex]::Escape($required)) {
      throw "bash package script must include release source gate: $required"
    }
  }
  if ($packageBash -match 'BASH_SOURCE') {
    throw 'bash package script must resolve its directory from portable $0 semantics'
  }
  if ($packageBash -match 'MSYS2_(?:ARG|ENV)_CONV_EXCL|MSYS_NO_PATHCONV|JUHE_AI_FRONTEND_API_BASE_URL') {
    throw 'bash package script must reject MSYS instead of maintaining path-conversion compatibility'
  }
  if ([regex]::Matches($packageBash, 'assert-release-source\.sh').Count -lt 2) {
    throw 'bash package script must recheck the release source after the build'
  }
  if ($packageBash.IndexOf('copy_required_item "$REPO_ROOT/frontend/dist"', [StringComparison]::Ordinal) -gt $packageBash.IndexOf('node "$VALIDATOR_PATH" --quiet --deploy-mode=go "$PACKAGE_ROOT"', [StringComparison]::Ordinal)) {
    throw 'bash package script must validate the completed frontend bundle before archive creation'
  }

  Write-Output 'release source preflight regression passed'
} finally {
  $resolvedTarget = [System.IO.Path]::GetFullPath($tempRoot)
  if (-not $resolvedTarget.StartsWith($resolvedTempBase, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "refusing to remove unexpected path: $resolvedTarget"
  }
  Remove-Item -LiteralPath $resolvedTarget -Recurse -Force -ErrorAction SilentlyContinue
}
