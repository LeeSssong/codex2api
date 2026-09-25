param(
    [ValidateSet('start', 'stop', 'status')]
    [string]$Action = 'start',
    [ValidateRange(1024, 65535)]
    [int]$BackendPort = 8080,
    [ValidateRange(1024, 65535)]
    [int]$FrontendPort = 5173
)

$ErrorActionPreference = 'Stop'
$projectRoot = $PSScriptRoot
$devDir = Join-Path $projectRoot '.dev'
$statePath = Join-Path $devDir 'processes.json'
$airPath = Join-Path $devDir 'tools/air.exe'

function Get-OwnedProcess($entry) {
    $process = Get-Process -Id $entry.Id -ErrorAction SilentlyContinue
    $expectedPath = $entry.Path
    if ([string]::IsNullOrWhiteSpace($expectedPath)) {
        $expectedPath = switch ($entry.Service) {
            'backend' { $airPath }
            'frontend' { (Get-Command node.exe -ErrorAction SilentlyContinue).Source }
        }
    }
    if ($process -and $expectedPath -and $process.Path -eq $expectedPath -and
        $process.StartTime.ToUniversalTime().Ticks.ToString() -eq $entry.StartTicks) {
        return $process
    }
}

function Save-Processes($entries) {
    $entries | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath $statePath -Encoding utf8
}

$entries = @()
if (Test-Path -LiteralPath $statePath) {
    $entries = @(Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json)
}

if ($Action -eq 'status') {
    foreach ($entry in $entries) {
        [pscustomobject]@{ Service = $entry.Service; PID = $entry.Id;
            Running = [bool](Get-OwnedProcess $entry); URL = $entry.URL }
    }
    if (-not $entries) { Write-Host 'Development services are stopped.' }
    exit 0
}

if ($Action -eq 'stop') {
    foreach ($entry in $entries) {
        if (Get-OwnedProcess $entry) {
            & taskkill.exe /PID $entry.Id /T /F | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "Could not stop $($entry.Service)." }
        }
    }
    if (Test-Path -LiteralPath $statePath) { Remove-Item -LiteralPath $statePath }
    Write-Host 'Development services stopped.'
    exit 0
}

if (@($entries | Where-Object { Get-OwnedProcess $_ }).Count -gt 0) {
    throw 'Development services are already running. Use -Action status or -Action stop.'
}
if ($BackendPort -eq $FrontendPort) { throw 'The two ports must be different.' }
foreach ($port in @($BackendPort, $FrontendPort)) {
    if (Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue) {
        throw "Port $port is occupied. Choose another -BackendPort or -FrontendPort."
    }
}

New-Item -ItemType Directory -Path $devDir, (Join-Path $devDir 'tools'),
    (Join-Path $devDir 'tmp'), (Join-Path $projectRoot 'data') -Force | Out-Null
$nodePath = (Get-Command node.exe).Source
$previousPath = $env:PATH
$previousGoRoot = $env:GOROOT
Push-Location $projectRoot
try {
    $localGoBin = Join-Path $devDir 'go/bin'
    if (Test-Path -LiteralPath (Join-Path $localGoBin 'go.exe')) {
        $env:PATH = "$localGoBin;$env:PATH"
        $env:GOROOT = Join-Path $devDir 'go'
    }
    if (-not (Test-Path -LiteralPath $airPath)) {
        $previousGoBin = $env:GOBIN
        try {
            $env:GOBIN = Join-Path $devDir 'tools'
            & go install github.com/air-verse/air@v1.67.4
            if ($LASTEXITCODE -ne 0) { throw 'Air installation failed.' }
        } finally { $env:GOBIN = $previousGoBin }
    }
    Push-Location (Join-Path $projectRoot 'frontend')
    try {
        if (-not (Test-Path -LiteralPath 'node_modules/vite/bin/vite.js')) {
            & npm.cmd ci --no-audit --no-fund
            if ($LASTEXITCODE -ne 0) { throw 'Frontend dependency installation failed.' }
        }
        if (-not (Test-Path -LiteralPath 'dist/index.html')) {
            & npm.cmd run build
            if ($LASTEXITCODE -ne 0) { throw 'Initial frontend build failed.' }
        }
    } finally { Pop-Location }

    $previousPort = $env:CODEX_PORT
    $previousBind = $env:CODEX_BIND
    $previousTarget = $env:VITE_API_TARGET
    $entries = @()
    try {
        $env:CODEX_PORT = $BackendPort.ToString()
        $env:CODEX_BIND = '127.0.0.1'
        $env:VITE_API_TARGET = "http://127.0.0.1:$BackendPort"
        $services = @(
            @{ Name = 'backend'; File = $airPath; Args = @('-c', '.air.toml');
               Cwd = $projectRoot; URL = "http://127.0.0.1:$BackendPort/health" },
            @{ Name = 'frontend'; File = $nodePath;
               Args = @('node_modules/vite/bin/vite.js', '--host', '127.0.0.1', '--port', $FrontendPort.ToString(), '--strictPort');
               Cwd = (Join-Path $projectRoot 'frontend'); URL = "http://127.0.0.1:$FrontendPort/admin/" }
        )
        foreach ($service in $services) {
            $process = Start-Process -FilePath $service.File -ArgumentList $service.Args `
                -WorkingDirectory $service.Cwd -WindowStyle Hidden -PassThru `
                -RedirectStandardOutput (Join-Path $devDir "$($service.Name).stdout.log") `
                -RedirectStandardError (Join-Path $devDir "$($service.Name).stderr.log")
            $entries += [pscustomobject]@{ Service = $service.Name; Id = $process.Id;
                Path = $service.File; StartTicks = $process.StartTime.ToUniversalTime().Ticks.ToString(); URL = $service.URL }
            Save-Processes $entries
        }
    } catch {
        foreach ($entry in $entries) {
            if (Get-OwnedProcess $entry) { & taskkill.exe /PID $entry.Id /T /F | Out-Null }
        }
        throw
    } finally {
        $env:CODEX_PORT = $previousPort
        $env:CODEX_BIND = $previousBind
        $env:VITE_API_TARGET = $previousTarget
    }
    Write-Host "Frontend: http://127.0.0.1:$FrontendPort/admin/"
    Write-Host "API: http://127.0.0.1:$BackendPort/v1"
    Write-Host "Air is compiling the backend. Logs: $devDir"
    Write-Host 'First visit: initialize an admin password in the browser.'
} finally {
    $env:PATH = $previousPath
    $env:GOROOT = $previousGoRoot
    Pop-Location
}
