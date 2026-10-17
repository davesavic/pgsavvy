[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'cli-postgres.ps1'), [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'fixture script does not parse' }
$helper = $ast.Find({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Invoke-Checked' }, $true)
if (-not $helper) { throw 'native command helper missing' }
. ([scriptblock]::Create($helper.Extent.Text))
$fixtureTry = @($ast.EndBlock.Statements | Where-Object { $_ -is [Management.Automation.Language.TryStatementAst] })[0]
$failureBody = $fixtureTry.CatchClauses[0].Body.Extent.Text
$pwsh = Join-Path $PSHOME 'pwsh'
if ($IsWindows) { $pwsh += '.exe' }
$temp = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
$root = Join-Path $temp ('pgsavvy-script-test.' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $root
$helperCleanupUncertain = $false
try {
    $log = Join-Path $root 'events.jsonl'
    $producer = '[Console]::Out.WriteLine(''{"Action":"pass","Test":"fixture"}''); [Console]::Error.WriteLine(''synthetic-secret-sentinel''); exit 0'
    Invoke-Checked $pwsh @('-NoProfile', '-NonInteractive', '-Command', $producer) $log
    $events = @(Get-Content $log | ForEach-Object { $_ | ConvertFrom-Json })
    if ($events.Count -ne 1 -or $events[0].Action -ne 'pass' -or $events[0].Test -ne 'fixture') {
        throw 'stdout evidence changed'
    }
    if ((Get-Content -Raw "$log.err").Trim() -cne 'synthetic-secret-sentinel') { throw 'private stderr not captured separately' }
    $rejected = $false
    try {
        Invoke-Checked $pwsh @('-NoProfile', '-NonInteractive', '-Command', 'exit 7') (Join-Path $root 'failed')
    } catch { $rejected = $true }
    if (-not $rejected) { throw 'native command failure accepted' }

    $diagnostic = Join-Path $root 'diagnostic.ps1'
    $source = '$stage = ''tests''; try { throw ''postgres://user:synthetic-secret-sentinel@localhost/db'' } catch ' + $failureBody + "`nif (`$failed) { exit 1 }"
    [IO.File]::WriteAllText($diagnostic, $source)
    $out = Join-Path $root 'public.out'
    $err = Join-Path $root 'public.err'
    & $pwsh -NoProfile -NonInteractive -File $diagnostic > $out 2> $err
    if ($LASTEXITCODE -ne 1) { throw 'fixture failure status lost' }
    if ((Get-Item $out).Length -ne 0) { throw 'failure emitted stdout' }
    $message = (Get-Content -Raw $err).Trim()
    if ($message -cnotmatch '^native fixture preflight failed \(stage=tests; line=[0-9]+\); no native support evidence established$') {
        throw 'failure diagnostic is not allowlisted stage/line only'
    }

    # Execute the real startup path with a failing native probe, before any
    # fixture directory/download is reached. Bypass only the Windows gate.
    $fixtureSource = $ast.Extent.Text
    $guard = 'if (-not $IsWindows)'
    if (-not $fixtureSource.Contains($guard)) { throw 'Windows guard missing' }
    $fixtureSource = $fixtureSource.Replace($guard, 'if ($false)')
    $fixture = Join-Path $root 'fixture.ps1'
    [IO.File]::WriteAllText($fixture, $fixtureSource)
    $stub = 'function go { & ' + "'" + $pwsh.Replace("'", "''") + "'" + ' -NoProfile -NonInteractive -Command ''[Console]::Error.WriteLine("synthetic-secret-sentinel"); exit 7'' }' + "`n"
    $stub += "& '" + $fixture.Replace("'", "''") + "' -Major 17`nexit `$LASTEXITCODE"
    $probe = Join-Path $root 'probe.ps1'
    [IO.File]::WriteAllText($probe, $stub)
    & $pwsh -NoProfile -NonInteractive -File $probe > $out 2> $err
    if ($LASTEXITCODE -ne 1) { throw 'failing probe status lost' }
    if ((Get-Item $out).Length -ne 0) { throw 'failing probe emitted stdout' }
    $message = (Get-Content -Raw $err).Trim()
    if ($message -cnotmatch '^native fixture preflight failed \(stage=toolchain; line=[0-9]+\); no native support evidence established$') {
        throw 'native probe stderr escaped sanitized failure reporting'
    }

    $ownedRoot = Join-Path $root ('pgsavvy-native.' + [guid]::NewGuid().ToString('N'))
    $null = New-Item -ItemType Directory -Path $ownedRoot
    [IO.File]::WriteAllText((Join-Path $ownedRoot 'init.log'), 'synthetic-secret-sentinel')
    $source = '$ErrorActionPreference = ''Stop''; $root = ''' + $ownedRoot.Replace("'", "''") + '''; $owned = $true; $pg = $null; $stage = ''initdb''; $script:lastNativeExit = 7; try { throw ''synthetic-secret-sentinel'' } catch ' + $failureBody + ' finally ' + $fixtureTry.Finally.Extent.Text + "`nif (`$failed) { exit 1 }"
    [IO.File]::WriteAllText($diagnostic, $source)
    & $pwsh -NoProfile -NonInteractive -File $diagnostic > $out 2> $err
    if ($LASTEXITCODE -ne 1 -or (Get-Item $out).Length -ne 0) { throw 'cleanup failure status/stdout changed' }
    if (Test-Path -LiteralPath $ownedRoot) { throw 'owned diagnostic logs were not cleaned up' }
    if ((Get-Content -Raw $err).Trim() -cnotmatch '^native fixture preflight failed \(stage=initdb; line=[0-9]+; native_exit=7\); no native support evidence established$') {
        throw 'numeric initdb diagnostic did not survive owned cleanup'
    }
    $null = New-Item -ItemType Directory -Path $ownedRoot
    $source = '$ErrorActionPreference = ''Stop''; $root = ''' + $ownedRoot.Replace("'", "''") + '''; $owned = $true; $pg = $null; $probeCleanupFailed = $true; $stage = ''initdb-probe''; try { throw ''synthetic-secret-sentinel'' } catch ' + $failureBody + ' finally ' + $fixtureTry.Finally.Extent.Text + "`nif (`$failed) { exit 1 }"
    [IO.File]::WriteAllText($diagnostic, $source)
    & $pwsh -NoProfile -NonInteractive -File $diagnostic > $out 2> $err
    if ($LASTEXITCODE -ne 1 -or -not (Test-Path -LiteralPath $ownedRoot)) { throw 'main cleanup deleted unjoined probe data' }
    $messages = @(Get-Content $err)
    if ($messages.Count -ne 2 -or $messages[0] -cnotmatch '^native fixture preflight failed \(stage=initdb-probe; line=[0-9]+\); no native support evidence established$' -or $messages[1] -cne 'owned fixture cleanup failed; retained owned fixture') {
        throw 'probe retention disclosed private diagnostics'
    }
    & (Join-Path $PSScriptRoot 'cli-initdb-probe.test.ps1') -CleanupUncertain ([ref]$helperCleanupUncertain)
    & (Join-Path $PSScriptRoot 'cli-initdb-fixture.test.ps1') -CleanupUncertain ([ref]$helperCleanupUncertain)
    Write-Output 'native fixture script regressions passed'
} finally {
    if (-not $helperCleanupUncertain) { Remove-Item -LiteralPath $root -Recurse -Force }
}
