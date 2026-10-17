[CmdletBinding()]
param([ref]$CleanupUncertain)
if ($null -eq $CleanupUncertain) { $localCleanupUncertain = $false; $CleanupUncertain = [ref]$localCleanupUncertain }
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$tokens = $null
$parseErrors = $null
$module = Join-Path $PSScriptRoot 'cli-initdb-probe.ps1'
$null = [Management.Automation.Language.Parser]::ParseFile($module, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'initdb probe does not parse' }
. $module
$temp = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
$root = Join-Path $temp ('pgsavvy-probe-test.' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $root
try {
    $fakePg = Join-Path $root 'fake postgres'
    $null = New-Item -ItemType Directory -Path $fakePg
    $source = @'
package main
import (
 "encoding/json"
 "fmt"
 "io"
 "os"
 "os/exec"
 "path/filepath"
 "strings"
 "time"
)
func main() {
 if len(os.Args) > 1 {
  switch os.Args[1] {
  case "--version": fmt.Println("synthetic version"); return
  case "--sleep": time.Sleep(time.Minute); return
  case "--stdin":
   input, err := io.ReadAll(os.Stdin)
   if err != nil || len(input) != 0 { os.Exit(76) }
   fmt.Println("stdin-eof"); return
  case "--tree-sleep":
   child := exec.Command(os.Args[0], "--sleep")
   child.Stdout, child.Stderr = os.Stdout, os.Stderr
   if err := child.Start(); err != nil { os.Exit(75) }
   fmt.Println("child-ready")
   time.Sleep(time.Minute); return
  case "--flood": fmt.Print(strings.Repeat("x", 70000)); fmt.Fprint(os.Stderr, strings.Repeat("y", 70000)); return
  case "--argv":
   json.NewEncoder(os.Stdout).Encode(map[string]any{"args":os.Args[2:], "secret":os.Getenv("PGPASSWORD"), "token":os.Getenv("PROBE_SECRET"), "keep":os.Getenv("PROBE_KEEP")})
   os.Exit(7)
  }
 }
  if len(os.Args) != 11 || os.Args[1] != "-D" || os.Args[3] != "-U" || os.Args[4] != "pgsavvy" || os.Args[5] != "--encoding=UTF8" || os.Args[6] != "--no-locale" || os.Args[7] != "--auth-local=scram-sha-256" || os.Args[8] != "--auth-host=scram-sha-256" || os.Args[9] != "--no-instructions" || !strings.HasPrefix(os.Args[10], "--pwfile=") { os.Exit(71) }
  password, err := os.ReadFile(strings.TrimPrefix(os.Args[10], "--pwfile="))
 if err != nil || string(password) != "pgsavvy-public-diagnostic-only\n" { os.Exit(72) }
 if os.Getenv("PGPASSWORD") != "" || os.Getenv("PROBE_SECRET") != "" { os.Exit(73) }
 if err := os.WriteFile(filepath.Join(filepath.Dir(os.Args[0]), "probe-invoked"), []byte("no listening server"), 0600); err != nil { os.Exit(74) }
 fmt.Fprintf(os.Stdout, "synthetic initialization in %s\n", os.Args[2])
 fmt.Fprintf(os.Stderr, "\x1b[31m::error::##[error]unrecognized synthetic bootstrap failure in %s\n", os.Args[2])
 os.Exit(7)
}
'@
    $goFile = Join-Path $root 'probe.go'
    [IO.File]::WriteAllText($goFile, $source, [Text.UTF8Encoding]::new($false))
    $binary = Join-Path $fakePg 'initdb.exe'
    & go build -o $binary $goFile > (Join-Path $root 'build.out') 2> (Join-Path $root 'build.err')
    if ($LASTEXITCODE -ne 0) { throw 'native probe test helper build failed' }
    Copy-Item -LiteralPath $binary -Destination (Join-Path $fakePg 'postgres.exe')
    $windows = if ($IsWindows) { [Environment]::GetFolderPath([Environment+SpecialFolder]::Windows) } else { Join-Path $root 'windows' }
    $environmentRoot = Join-Path $root 'environment'
    $null = New-Item -ItemType Directory -Path $environmentRoot
    $environment = Get-ProbeEnvironment $fakePg $environmentRoot $windows
    $environment.PROBE_KEEP = 'kept'
    if ($environment.ContainsKey('PGPASSWORD') -or $environment.ContainsKey('PROBE_SECRET')) { throw 'probe environment inherited secrets' }
    $oldPassword = $env:PGPASSWORD
    $oldSecret = $env:PROBE_SECRET
    try {
        $env:PGPASSWORD = 'synthetic-private-password'
        $env:PROBE_SECRET = 'synthetic-private-token'
        $arguments = @('space here', 'quote"here', '', '--pwfile=C:\private path\password', ';Write-Output injected')
        $result = Invoke-ProbeCommand $binary (@('--argv') + $arguments) $environment $root
        if ($result.cleanup_failed) { $CleanupUncertain.Value = $true }
        if (-not $result.started -or $result.exit_code -ne 7 -or $result.capture_failed -or $result.cleanup_failed) { throw 'native exit/capture evidence lost' }
        $report = $result.streams[0].text | ConvertFrom-Json
        if ($report.secret -or $report.token -or $report.keep -cne 'kept') { throw 'child environment isolation failed' }
        if ($report.args.Count -ne $arguments.Count) { throw 'native argument count changed' }
        for ($i = 0; $i -lt $arguments.Count; $i++) {
            if ($report.args[$i] -cne $arguments[$i]) { throw 'native argument changed' }
        }
        if ($env:PGPASSWORD -cne 'synthetic-private-password' -or $env:PROBE_SECRET -cne 'synthetic-private-token') { throw 'probe mutated parent environment' }

        $writer = [IO.StringWriter]::new()
        $savedOut = [Console]::Out
        try {
            [Console]::SetOut($writer)
            $success = Invoke-InitdbProbe $fakePg $root $root $windows $CleanupUncertain
        } finally { [Console]::SetOut($savedOut) }
        $published = $writer.ToString()
        $writer.Dispose()
        if ($success -isnot [bool] -or $success -or $CleanupUncertain.Value) { throw 'synthetic initialization failure accepted' }
        if (Test-Path (Join-Path $root 'initdb-probe')) { throw 'probe owned directory not cleaned up' }
        if (-not (Test-Path (Join-Path $fakePg 'probe-invoked'))) { throw 'initialization probe did not execute' }
        foreach ($secret in @($root, 'synthetic-private-password', 'synthetic-private-token', [string][char]27, '##[')) {
            if ($published.Contains($secret)) { throw 'private path/secret/control character published' }
        }
        $records = @($published.Trim().Split("`n") | ForEach-Object { $_ | ConvertFrom-Json })
        if ($records.Count -ne 3 -or $records[0].command -ne 'initdb-version' -or $records[1].command -ne 'postgres-version' -or $records[2].command -ne 'initialize') { throw 'probe inventory incomplete' }
        if ($records[2].exit_code -ne 7 -or $records[2].exit_hex -cne '00000007' -or $records[2].output[1].text -notlike '*unrecognized synthetic bootstrap failure*') { throw 'actual unknown native diagnostic was lost' }
    } finally {
        $env:PGPASSWORD = $oldPassword
        $env:PROBE_SECRET = $oldSecret
    }

    $protected = Protect-ProbeText 'C:\PRIVATE\profile AlphaUser BetaUserX C:/private/profile' $false @('C:\private\profile', 'AlphaUser', 'BetaUserX')
    if ($protected.text -cne '*** *** *** ***') { throw 'case/slash/equal-length redaction failed' }
    $protected = Protect-ProbeText ("completed`nprivate-path-prefix") $true @('private-path-prefix-rest')
    if ($protected.text -cne "completed`n") { throw 'partial captured path was published' }
    $protected = Protect-ProbeText ('x' * 6000) $false @()
    if ($protected.text.Length -gt 4096 -or -not $protected.truncated) { throw 'publication limit missing' }

    $result = Invoke-ProbeCommand $binary @('--flood') $environment $root
    if ($result.cleanup_failed) { $CleanupUncertain.Value = $true }
    if ($result.exit_code -ne 0 -or $result.capture_failed -or $result.cleanup_failed) { throw 'oversized output blocked child' }
    foreach ($stream in $result.streams) {
        if ($stream.captured_chars -ne 65536 -or -not $stream.truncated) { throw 'capture limit missing' }
        $protected = Protect-ProbeText $stream.text $stream.truncated @()
        if ($protected.text -ne '') { throw 'partial oversized line published' }
    }
    $result = Invoke-ProbeCommand (Join-Path $root 'missing.exe') @() $environment $root
    if ($result.cleanup_failed) { $CleanupUncertain.Value = $true }
    if ($result.started -or $null -eq $result.launch_error -or $null -ne $result.exit_code) { throw 'launch failure confused with native exit' }
    $result = Invoke-ProbeCommand $binary @('--stdin') $environment $root
    if ($result.cleanup_failed) { $CleanupUncertain.Value = $true }
    if ($result.exit_code -ne 0 -or $result.capture_failed -or $result.cleanup_failed -or $result.streams[0].text.Trim() -cne 'stdin-eof') { throw 'probe stdin was not closed' }
    $result = Invoke-ProbeCommand $binary @('--sleep') $environment $root 200
    if ($result.cleanup_failed) { $CleanupUncertain.Value = $true }
    if (-not $result.started -or -not $result.timed_out -or $result.capture_failed -or $result.cleanup_failed) { throw 'probe timeout did not stop/join owned process' }
    $result = Invoke-ProbeCommand $binary @('--tree-sleep') $environment $root 3000
    if ($result.cleanup_failed) { $CleanupUncertain.Value = $true }
    if (-not $result.timed_out -or $result.capture_failed -or $result.cleanup_failed -or $result.streams[0].text -notlike '*child-ready*') { throw 'probe timeout did not join bootstrap descendant pipes' }

    $originalCommand = (Get-Item Function:Invoke-ProbeCommand).ScriptBlock
    $simulatedCleanup = $false
    $writer = [IO.StringWriter]::new()
    $savedOut = [Console]::Out
    try {
        function Invoke-ProbeCommand { return [pscustomobject]@{ started = $true; exit_code = 0; launch_error = $null; timed_out = $false; capture_failed = $true; cleanup_failed = $true; streams = @() } }
        [Console]::SetOut($writer)
        $success = Invoke-InitdbProbe $fakePg $root $root $windows ([ref]$simulatedCleanup)
    } finally {
        [Console]::SetOut($savedOut)
        Set-Item Function:Invoke-ProbeCommand -Value $originalCommand
        $writer.Dispose()
    }
    if ($success -or -not $simulatedCleanup -or -not (Test-Path (Join-Path $root 'initdb-probe'))) { throw 'uncertain probe cleanup did not retain owned data' }
    # The mock launched no process, so its retained test data is safe to remove.
    Remove-Item -LiteralPath (Join-Path $root 'initdb-probe') -Recurse -Force

    $mainAst = [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'cli-postgres.ps1'), [ref]$tokens, [ref]$parseErrors)
    $probeCall = $mainAst.Find({ param($n) $n -is [Management.Automation.Language.CommandAst] -and $n.GetCommandName() -eq 'Invoke-InitdbProbe' }, $true)
    $password = $mainAst.Find({ param($n) $n -is [Management.Automation.Language.AssignmentStatementAst] -and $n.Left.Extent.Text -eq '$password' }, $true)
    if (-not $probeCall -or -not $password -or $probeCall.Extent.StartOffset -ge $password.Extent.StartOffset) { throw 'probe reached real fixture credential generation' }
    if ((Get-Content -Raw $module).Contains('pg_ctl')) { throw 'diagnostic probe can start a listening server' }
    Write-Output 'initdb probe regressions passed'
} finally {
    if (-not $CleanupUncertain.Value) { Remove-Item -LiteralPath $root -Recurse -Force }
}
