$ErrorActionPreference = "Stop"
if (!$env:JAVA_HOME -or !(Test-Path (Join-Path $env:JAVA_HOME "bin\java.exe"))) {
    $java = Get-Command java -ErrorAction Stop
    $env:JAVA_HOME = Split-Path (Split-Path $java.Source -Parent) -Parent
}
$launcher = Join-Path $PSScriptRoot "build\install\cookist-core\bin\cookist-core.bat"
if (!(Test-Path $launcher)) { throw "Build first: .\scripts\gradle.ps1 -ProjectDir services\core installDist" }
& $launcher
exit $LASTEXITCODE
