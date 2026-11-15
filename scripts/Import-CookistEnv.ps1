function Import-CookistEnv {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    $ErrorActionPreference = 'Stop'
    if (-not (Test-Path -LiteralPath $Path)) { return }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw 'The environment configuration path must be a file.'
    }

    $values = [System.Collections.Generic.Dictionary[string,string]]::new([System.StringComparer]::OrdinalIgnoreCase)
    $lineNumber = 0
    foreach ($line in [System.IO.File]::ReadAllLines((Resolve-Path -LiteralPath $Path).ProviderPath)) {
        $lineNumber++
        $text = $line.Trim()
        if ($text.Length -eq 0 -or $text.StartsWith('#')) { continue }
        if ($text -notmatch '^([A-Za-z_][A-Za-z0-9_]*)=(.*)$') {
            throw "Invalid environment assignment at line $lineNumber. Use KEY=value."
        }
        $name = $Matches[1]
        $value = $Matches[2].Trim()
        if ($values.ContainsKey($name)) {
            throw "Duplicate environment variable at line $lineNumber."
        }
        if ($value.StartsWith('"') -or $value.StartsWith("'")) {
            $quote = $value.Substring(0, 1)
            if ($value.Length -lt 2 -or -not $value.EndsWith($quote)) {
                throw "Unclosed quoted environment value at line $lineNumber."
            }
            $value = $value.Substring(1, $value.Length - 2)
        }
        if ($value.Contains([string][char]0)) {
            throw "Invalid environment value at line $lineNumber."
        }
        $values.Add($name, $value)
    }

    # Validate the entire file before mutating the process; never evaluate its contents.
    $existing = [Environment]::GetEnvironmentVariables('Process')
    foreach ($entry in $values.GetEnumerator()) {
        if (-not $existing.Contains($entry.Key) -and $entry.Value.Length -gt 0) {
            [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
        }
    }
}
