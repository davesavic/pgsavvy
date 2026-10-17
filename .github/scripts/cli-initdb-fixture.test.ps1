[CmdletBinding()]
param([ref]$CleanupUncertain)
if ($null -eq $CleanupUncertain) { $localCleanupUncertain = $false; $CleanupUncertain = [ref]$localCleanupUncertain }
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
. (Join-Path $PSScriptRoot 'cli-initdb-probe.ps1')
$tokens = $null
$errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'cli-postgres.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { throw 'fixture script does not parse' }
$helper = $ast.Find({ param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Invoke-Checked' }, $true)
. ([scriptblock]::Create($helper.Extent.Text))
$body = @($ast.EndBlock.Statements | Where-Object { $_ -is [Management.Automation.Language.TryStatementAst] })[0].Body.Statements
$start = -1
$end = -1
for ($i = 0; $i -lt $body.Count; $i++) {
    if ($body[$i].Extent.Text -ceq '$stage = ''initdb''') { $start = $i }
    if ($body[$i].Extent.Text -ceq '$stage = ''startup''') { $end = $i }
}
if ($start -lt 0 -or $end -le $start + 1) { throw 'production initialization block missing' }
$initialize = [scriptblock]::Create(($body[($start + 1)..($end - 1)] | ForEach-Object { $_.Extent.Text }) -join "`n")
$temp = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
$testRoot = Join-Path $temp ('pgsavvy-fixture-test.' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $testRoot
$oldSentinel = $env:PGSAVVY_FIXTURE_AMBIENT_SENTINEL
$oldRestriction = $env:PG_RESTRICT_EXEC
try {
    $pg = Join-Path $testRoot 'fake postgres'
    $null = New-Item -ItemType Directory -Path $pg
    $source = @'
package main
import (
 "fmt"
 "io"
 "os"
 "path/filepath"
 "regexp"
 "strings"
)
func main() {
 if len(os.Args) != 10 || os.Args[1] != "-D" || os.Args[3] != "-U" || os.Args[4] != "pgsavvy" || os.Args[5] != "--encoding=UTF8" || os.Args[6] != "--no-locale" || os.Args[7] != "--auth-local=scram-sha-256" || os.Args[8] != "--auth-host=scram-sha-256" || !strings.HasPrefix(os.Args[9], "--pwfile=") { os.Exit(71) }
 password, err := os.ReadFile(strings.TrimPrefix(os.Args[9], "--pwfile="))
 if err != nil || !regexp.MustCompile(`^[0-9a-f]{64}\n$`).Match(password) { os.Exit(72) }
 if os.Getenv("PGSAVVY_FIXTURE_AMBIENT_SENTINEL") != "" || os.Getenv("PG_RESTRICT_EXEC") != "" { os.Exit(73) }
 cwd, err := os.Getwd()
 if err != nil || cwd != filepath.Join(filepath.Dir(os.Args[2]), "initdb-context") { os.Exit(74) }
 for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP"} {
  if !strings.HasPrefix(os.Getenv(key), cwd+string(os.PathSeparator)) { os.Exit(75) }
 }
 input, err := io.ReadAll(os.Stdin)
 if err != nil || len(input) != 0 { os.Exit(76) }
 fmt.Printf("private initialization stdout %s", password)
 fmt.Fprintf(os.Stderr, "private initialization stderr %s", password)
 if err := os.WriteFile(filepath.Join(filepath.Dir(os.Args[0]), "fixture-invoked"), []byte("isolated initialization"), 0600); err != nil { os.Exit(77) }
 mode, _ := os.ReadFile(filepath.Join(filepath.Dir(os.Args[0]), "mode"))
 if string(mode) == "fail" { os.Exit(7) }
}
'@
    $goFile = Join-Path $testRoot 'fixture.go'
    [IO.File]::WriteAllText($goFile, $source, [Text.UTF8Encoding]::new($false))
    & go build -o (Join-Path $pg 'initdb.exe') $goFile > (Join-Path $testRoot 'build.out') 2> (Join-Path $testRoot 'build.err')
    if ($LASTEXITCODE -ne 0) { throw 'fixture test helper build failed' }
    $windowsDirectory = if ($IsWindows) { [Environment]::GetFolderPath([Environment+SpecialFolder]::Windows) } else { Join-Path $testRoot 'windows' }
    $env:PGSAVVY_FIXTURE_AMBIENT_SENTINEL = 'synthetic-private-environment'
    $env:PG_RESTRICT_EXEC = '1'
    foreach ($mode in @('success', 'fail')) {
        $root = Join-Path $testRoot $mode
        $null = New-Item -ItemType Directory -Path $root
        $data = Join-Path $root 'data'
        $password = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).ToLowerInvariant()
        $passwordFile = Join-Path $root 'password'
        [IO.File]::WriteAllText($passwordFile, "$password`n", [Text.UTF8Encoding]::new($false))
        [IO.File]::WriteAllText((Join-Path $pg 'mode'), $mode)
        $probeCleanupFailed = $false
        $script:lastNativeExit = $null
        $out = [IO.StringWriter]::new()
        $err = [IO.StringWriter]::new()
        $savedOut = [Console]::Out
        $savedErr = [Console]::Error
        $rejected = $false
        $emitted = @()
        try {
            [Console]::SetOut($out)
            [Console]::SetError($err)
            $emitted = @(. $initialize)
        } catch {
            $rejected = $true
            if ($mode -eq 'success') { throw 'production initialization did not use the isolated launch context' }
            if ($_.Exception.Message.Contains($password)) { throw 'initialization exception leaked password' }
        } finally {
            [Console]::SetOut($savedOut)
            [Console]::SetError($savedErr)
            if ($probeCleanupFailed) { $CleanupUncertain.Value = $true }
        }
        if ($out.ToString() -or $err.ToString() -or $emitted.Count -ne 0) { throw 'private initialization output was published' }
        $out.Dispose()
        $err.Dispose()
        if ($rejected -ne ($mode -eq 'fail') -or $script:lastNativeExit -ne $(if ($mode -eq 'fail') { 7 } else { 0 })) { throw 'initialization exit status lost' }
        if ($probeCleanupFailed -or -not (Test-Path (Join-Path $pg 'fixture-invoked'))) { throw 'fixture invocation/cleanup evidence missing' }
        if ($env:PGSAVVY_FIXTURE_AMBIENT_SENTINEL -cne 'synthetic-private-environment' -or $env:PG_RESTRICT_EXEC -cne '1') { throw 'initialization changed parent environment' }
    }
    $originalCommand = (Get-Item Function:Invoke-ProbeCommand).ScriptBlock
    try {
        foreach ($failure in @('launch', 'timeout', 'capture', 'cleanup')) {
            $root = Join-Path $testRoot $failure
            $null = New-Item -ItemType Directory -Path $root
            $data = Join-Path $root 'data'
            $passwordFile = Join-Path $root 'password'
            $script:fixtureLaunchResult = [pscustomobject]@{
                started = $failure -ne 'launch'; exit_code = $(if ($failure -eq 'launch') { $null } else { 0 })
                timed_out = $failure -eq 'timeout'; capture_failed = $failure -eq 'capture'
                cleanup_failed = $failure -eq 'cleanup'; streams = @()
            }
            function Invoke-ProbeCommand { return $script:fixtureLaunchResult }
            $probeCleanupFailed = $false
            $rejected = $false
            try { . $initialize } catch { $rejected = $true }
            if (-not $rejected) { throw 'incomplete initialization accepted' }
            if ($probeCleanupFailed -ne ($failure -eq 'cleanup')) { throw 'unjoined initialization did not retain its fixture' }
        }
    } finally {
        Set-Item Function:Invoke-ProbeCommand -Value $originalCommand
        Remove-Variable -Name fixtureLaunchResult -Scope Script
    }
    Write-Output 'private fixture initialization regressions passed'
} finally {
    $env:PGSAVVY_FIXTURE_AMBIENT_SENTINEL = $oldSentinel
    $env:PG_RESTRICT_EXEC = $oldRestriction
    if (-not $CleanupUncertain.Value) { Remove-Item -LiteralPath $testRoot -Recurse -Force }
}
