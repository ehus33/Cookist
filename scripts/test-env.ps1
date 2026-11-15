$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Import-CookistEnv.ps1')
$directory = Join-Path ([System.IO.Path]::GetTempPath()) ("cookist-env-test-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $directory | Out-Null
$file = Join-Path $directory '.env'
$prefix = 'COOKIST_ENV_TEST_' + [guid]::NewGuid().ToString('N').ToUpper() + '_'
$keys = @('PLAIN', 'QUOTED', 'SINGLE', 'EMPTY', 'LITERAL', 'EXISTING', 'ATOMIC') | ForEach-Object { $prefix + $_ }

function Assert-Equal($Actual, $Expected, [string]$Message) {
    if ($Actual -cne $Expected) { throw $Message }
}
function Assert-Rejected([string]$Content, [string]$Message) {
    [IO.File]::WriteAllText($file, $Content)
    $rejected = $false
    try { Import-CookistEnv -Path $file }
    catch {
        $rejected = $true
        if ($_.Exception.Message.Contains('private-test-value')) { throw 'Parser exposed a value in an error.' }
    }
    if (-not $rejected) { throw $Message }
}

try {
    [Environment]::SetEnvironmentVariable($prefix + 'EXISTING', 'process-wins', 'Process')
    $content = @(
        '# comment'
        ''
        "${prefix}PLAIN=hello=world"
        "${prefix}QUOTED=`"with spaces # retained`""
        "${prefix}SINGLE='single quoted'"
        "${prefix}EMPTY="
        ('{0}LITERAL=$(throw ''must not execute'')' -f $prefix)
        "${prefix}EXISTING=file-loses"
    ) -join "`n"
    [IO.File]::WriteAllText($file, $content)
    Import-CookistEnv -Path $file
    Assert-Equal ([Environment]::GetEnvironmentVariable($prefix + 'PLAIN')) 'hello=world' 'Assignment values were split incorrectly.'
    Assert-Equal ([Environment]::GetEnvironmentVariable($prefix + 'QUOTED')) 'with spaces # retained' 'Quoted value was damaged.'
    Assert-Equal ([Environment]::GetEnvironmentVariable($prefix + 'SINGLE')) 'single quoted' 'Single quotes were not removed.'
    Assert-Equal ([Environment]::GetEnvironmentVariable($prefix + 'EMPTY')) $null 'Empty placeholder should remain unset.'
    Assert-Equal ([Environment]::GetEnvironmentVariable($prefix + 'LITERAL')) '$(throw ''must not execute'')' 'Value was evaluated or interpolated.'
    Assert-Equal ([Environment]::GetEnvironmentVariable($prefix + 'EXISTING')) 'process-wins' 'File overrode process configuration.'
    Assert-Rejected "${prefix}ATOMIC=private-test-value`nnot an assignment" 'Malformed assignment accepted.'
    Assert-Equal ([Environment]::GetEnvironmentVariable($prefix + 'ATOMIC')) $null 'Invalid file partially changed environment.'
    Assert-Rejected "${prefix}ATOMIC=private-test-value`n${prefix}ATOMIC=other" 'Duplicate key accepted.'
    Assert-Rejected "${prefix}ATOMIC=`"private-test-value" 'Unclosed quote accepted.'
    Import-CookistEnv -Path (Join-Path $directory 'missing.env')
    Write-Output 'PASS: literal parsing, quotes, empty placeholders, process precedence, validation, atomicity, missing file, and secret-safe errors.'
} finally {
    foreach ($key in $keys) { [Environment]::SetEnvironmentVariable($key, $null, 'Process') }
    if (Test-Path -LiteralPath $file) { Remove-Item -LiteralPath $file }
    Remove-Item -LiteralPath $directory
}
