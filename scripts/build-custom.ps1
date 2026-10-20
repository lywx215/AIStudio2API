param(
    [ValidateSet('cli', 'desktop')]
    [string]$Target = 'cli',
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$')]
    [string]$Version = 'v0.2.14-custom.1',
    [switch]$SkipFrontend
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Push-Location $projectRoot
try {
    if (-not $SkipFrontend) {
        Push-Location (Join-Path $projectRoot 'web')
        try {
            npm ci
            if ($LASTEXITCODE -ne 0) { throw 'Frontend dependency installation failed' }
            npm run build
            if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed' }
        } finally { Pop-Location }
    }
    $outputDir = Join-Path $projectRoot 'dist'
    New-Item -ItemType Directory -Path $outputDir -Force | Out-Null
    $outputPath = Join-Path $outputDir "aistudio2api-$Target.exe"
    $linkFlags = "-s -w -X github.com/Mag1cFall/AIStudio2API/internal/app.version=$Version -X github.com/Mag1cFall/AIStudio2API/internal/app.managedBy=lywx215"
    $buildArgs = @('build', '-mod=readonly', '-trimpath')
    if ($Target -eq 'desktop') {
        $buildArgs += @('-tags', 'desktop,production')
        $linkFlags = "-H windowsgui $linkFlags"
    }
    $buildArgs += @('-ldflags', $linkFlags, '-o', $outputPath, './cmd/aistudio2api')
    & go @buildArgs
    if ($LASTEXITCODE -ne 0) { throw "Go $Target build failed" }
    Write-Output "Built $outputPath ($Version, managed by lywx215)"
} finally { Pop-Location }
