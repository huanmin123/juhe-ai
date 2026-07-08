$ErrorActionPreference = 'Stop'

$GoRoot = 'E:\gosdk\go1.26.5'
$W64DevkitBin = 'E:\tools\w64devkit-2.8.0\w64devkit\bin'
$GoCliBin = 'C:\Users\Administrator\go\bin'

foreach ($requiredPath in @($GoRoot, (Join-Path $GoRoot 'bin'), $W64DevkitBin, $GoCliBin)) {
    if (-not (Test-Path -LiteralPath $requiredPath)) {
        throw "Required Go toolchain path is missing: $requiredPath"
    }
}

$env:GOROOT = $GoRoot

$pathPrefix = @($W64DevkitBin, (Join-Path $GoRoot 'bin'), $GoCliBin)
$existingPath = @($env:Path -split ';' | Where-Object { $_ -and ($pathPrefix -notcontains $_) })
$env:Path = (($pathPrefix + $existingPath) -join ';')

$goVersion = (& go version)
if ($goVersion -notmatch 'go1\.26\.5') {
    throw "Unexpected Go version: $goVersion"
}

$gccVersion = (& gcc --version | Select-Object -First 1)
if ($gccVersion -notmatch '16\.1\.0') {
    throw "Unexpected GCC version: $gccVersion"
}

Write-Host "Go toolchain ready: $goVersion; $gccVersion"
