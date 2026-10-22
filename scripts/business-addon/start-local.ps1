param(
    [string]$SessionPath = (Join-Path $PSScriptRoot '../../../business-gemini-pool/business_gemini_session.json'),
    [string]$InstancePath = (Join-Path $PSScriptRoot '../../runtime/business-local'),
    [int]$Port = 2048
)
$ErrorActionPreference = 'Stop'
$repoPath = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
$instanceRoot = [IO.Path]::GetFullPath($InstancePath)
if (-not $instanceRoot.StartsWith($repoPath + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Local instance must remain inside this checkout.'
}
if (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue) {
    throw 'Requested local port is already in use. The existing process was not stopped.'
}
$executable = Join-Path $instanceRoot 'aistudio2api.exe'
if (-not (Test-Path -LiteralPath $executable)) { throw 'Build the local instance executable first.' }
$session = Get-Content -LiteralPath $SessionPath -Raw | ConvertFrom-Json
$modelKey = @($session.api_tokens)[0]
if ($modelKey -isnot [string] -or [string]::IsNullOrWhiteSpace($modelKey)) {
    throw 'The reference pool has no model API key.'
}
# Secrets exist only in this process and its child environment, never in args.
$settings = @{
    PROXY_API_KEY = $modelKey
    LISTEN_ADDR = "127.0.0.1:$Port"
    AUTO_START = 'false'
    ADMIN_AUTH_ENABLED = 'false'
    REQUEST_BODY_LOG = 'false'
    LOG_FILE = 'false'
    AISTUDIO_AUTH_STATES = 'auth'
}
$existingBrowser = Join-Path $repoPath 'runtime/camoufox/camoufox.exe'
if (Test-Path -LiteralPath $existingBrowser) {
    # The app validates its release version before using this existing cache.
    $settings['CAMOUFOX_PATH'] = $existingBrowser
}
$previous = @{}
try {
    foreach ($name in $settings.Keys) {
        $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        [Environment]::SetEnvironmentVariable($name, $settings[$name], 'Process')
    }
    $child = Start-Process -FilePath $executable -ArgumentList @('-open-ui=false', '-listen', "127.0.0.1:$Port") -WorkingDirectory $instanceRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $instanceRoot 'stdout.log') -RedirectStandardError (Join-Path $instanceRoot 'stderr.log')
    $ready = $false
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    while ([DateTime]::UtcNow -lt $deadline -and -not $child.HasExited) {
        try {
            $health = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/api/addons/business/status" -NoProxy -MaximumRedirection 0 -ConnectionTimeoutSeconds 1 -OperationTimeoutSeconds 1
            if ($health.StatusCode -eq 200) { $ready = $true; break }
        } catch { }
        Start-Sleep -Milliseconds 200
        $child.Refresh()
    }
    $record = @{ pid = $child.Id; origin = "http://127.0.0.1:$Port"; executable = $executable; instance = $instanceRoot; ready = $ready; started_at = [DateTime]::UtcNow.ToString('o') }
    $record | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $instanceRoot 'process.json') -Encoding utf8
    $record | ConvertTo-Json -Compress
    if (-not $ready) { throw 'The process did not become ready; inspect the local instance logs before retrying.' }
} finally {
    foreach ($name in $previous.Keys) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
    $session = $null
    $modelKey = $null
    $settings = $null
}
