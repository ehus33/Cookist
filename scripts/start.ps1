param([switch]$SkipBuild)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Net.Http
$root = Split-Path $PSScriptRoot -Parent
Set-Location $root
. (Join-Path $PSScriptRoot 'Import-CookistEnv.ps1')
Import-CookistEnv -Path (Join-Path $root '.env')
if (-not $env:JAVA_HOME -or -not (Test-Path -LiteralPath (Join-Path $env:JAVA_HOME 'bin\java.exe'))) {
    $env:JAVA_HOME = Split-Path (Split-Path (Get-Command java -ErrorAction Stop).Source -Parent) -Parent
}

function Run-Checked([scriptblock]$Command) {
    & $Command
    if ($LASTEXITCODE -ne 0) { throw "Build step failed with exit code $LASTEXITCODE." }
}

if (-not $SkipBuild) {
    if (-not (Test-Path -LiteralPath 'node_modules')) { Run-Checked { npm ci } }
    Run-Checked { npm run build }
    Run-Checked { & "$PSScriptRoot\gradle.ps1" -ProjectDir 'services\core' installDist }
    if (-not (Test-Path -LiteralPath '.tools')) { New-Item -ItemType Directory -Path '.tools' | Out-Null }
    Push-Location 'services\gateway'
    try { Run-Checked { go build -o '..\..\.tools\cookist-gateway.exe' . } }
    finally { Pop-Location }
}

$core = Join-Path $root 'services\core\build\install\cookist-core\bin\cookist-core.bat'
$gateway = Join-Path $root '.tools\cookist-gateway.exe'
foreach ($required in @($core, $gateway, (Join-Path $root 'apps\web\dist\index.html'))) {
    if (-not (Test-Path -LiteralPath $required)) { throw "Missing $required. Run without -SkipBuild." }
}
foreach ($port in @(8080, 8081)) {
    $connection = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue
    if ($connection) { throw "Port $port is already in use. Stop that service explicitly before starting Cookist." }
}
$children = @()
try {
    $children += Start-Process -FilePath $env:ComSpec -ArgumentList @('/c', "`"`"$core`"`"") -WorkingDirectory $root -NoNewWindow -PassThru
    $children += Start-Process -FilePath $gateway -WorkingDirectory $root -NoNewWindow -PassThru
    $ready = $false
    for ($i = 0; $i -lt 50; $i++) {
        foreach ($child in $children) {
            if ($child.HasExited) { throw "Cookist process $($child.Id) exited unexpectedly with code $($child.ExitCode)." }
        }
        try {
            $health = Invoke-RestMethod -Uri 'http://127.0.0.1:8080/health' -TimeoutSec 15
            if ($health.status -eq 'ok') { $ready = $true; break }
        } catch [System.Net.WebException] {
            Start-Sleep -Milliseconds 400
        } catch [System.Net.Http.HttpRequestException] {
            Start-Sleep -Milliseconds 400
        }
    }
    if (-not $ready) { throw 'Cookist did not become ready. See the service errors above.' }
    Write-Host "`nCookist is ready: http://localhost:8080"
    Write-Host 'Press Ctrl+C to stop the stack.'
    while ($true) {
        foreach ($child in $children) {
            if ($child.HasExited) { throw "Cookist process $($child.Id) exited unexpectedly." }
        }
        Start-Sleep -Seconds 1
    }
} finally {
    foreach ($child in $children) {
        if (-not $child.HasExited) {
            # Stop only descendants of the specific process launched in this invocation.
            $descendants = @(Get-CimInstance Win32_Process | Where-Object ParentProcessId -eq $child.Id)
            foreach ($descendant in $descendants) {
                Stop-Process -Id $descendant.ProcessId -ErrorAction Continue
            }
            Stop-Process -Id $child.Id -ErrorAction Continue
        }
    }
}
