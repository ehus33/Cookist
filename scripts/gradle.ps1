param(
    [string]$ProjectDir = ".",
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$GradleArgs
)
$ErrorActionPreference = "Stop"
$root = Split-Path $PSScriptRoot -Parent
if (!$env:JAVA_HOME -or !(Test-Path (Join-Path $env:JAVA_HOME "bin\java.exe"))) {
    $java = Get-Command java -ErrorAction Stop
    $env:JAVA_HOME = Split-Path (Split-Path $java.Source -Parent) -Parent
}
$gradle = Get-Command gradle -ErrorAction SilentlyContinue
if ($gradle) {
    $executable = $gradle.Source
} else {
    $cached = Get-ChildItem "$env:USERPROFILE\.gradle\wrapper\dists\gradle-8.13-bin" -Recurse -Filter gradle.bat -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($cached) {
        $executable = $cached.FullName
    } else {
        $tools = Join-Path $root ".tools"
        $executable = Join-Path $tools "gradle-8.13\bin\gradle.bat"
        if (!(Test-Path $executable)) {
            New-Item -ItemType Directory -Force $tools | Out-Null
            $zip = Join-Path $tools "gradle-8.13-bin.zip"
            Invoke-WebRequest "https://services.gradle.org/distributions/gradle-8.13-bin.zip" -OutFile $zip -UseBasicParsing
            $expected = (Invoke-WebRequest "https://services.gradle.org/distributions/gradle-8.13-bin.zip.sha256" -UseBasicParsing).Content.Trim()
            if ((Get-FileHash $zip -Algorithm SHA256).Hash -ine $expected) { throw "Gradle checksum mismatch" }
            Expand-Archive $zip -DestinationPath $tools -Force
            Remove-Item $zip
        }
    }
}
& $executable --console=plain -p $ProjectDir @GradleArgs
exit $LASTEXITCODE
