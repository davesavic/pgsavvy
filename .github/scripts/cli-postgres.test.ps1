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

    $classifier = $ast.Find({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Get-InitdbFailureCode' }, $true)
    if (-not $classifier) { throw 'initdb classifier missing' }
    . ([scriptblock]::Create($classifier.Extent.Text))
    $secret = 'synthetic-secret-sentinel'
    $cases = @(
        @('windows-admin', '', "Execution of PostgreSQL by a user with administrative permissions is not`r`npermitted."),
        @('windows-token', '', 'initdb: error: could not create restricted token: error code 5'),
        @('windows-token', '', "initdb: error: could not start process for command `"$secret`": error code 5"),
        @('child-exec', '', "initdb: error: could not execute command `"$secret`": Permission denied"),
        @('file-io', '', "initdb: error: could not open file `"C:\$secret\password`" for reading: Permission denied"),
        @('input-config', '', 'initdb: error: invalid locale settings; check LANG and LC_* environment variables'),
        @('child-io', 'running bootstrap script ... could not write to child process: Broken pipe', ''),
        @('unknown', $secret, "postgres://user:${secret}@localhost/db"),
        @('unknown', '', "initdb: error: unrecognized failure $secret"),
        @('unknown', '', 'untrusted prefix initdb: error: encoding mismatch'),
        @('unknown', '', ('x' * 65537))
    )
    $log = Join-Path $root 'init.log'
    $diagnostic = Join-Path $root 'initdb-diagnostic.ps1'
    foreach ($case in $cases) {
        [IO.File]::WriteAllText($log, $case[1], [Text.UTF8Encoding]::new($false))
        [IO.File]::WriteAllText("$log.err", $case[2], [Text.UTF8Encoding]::new($false))
        $source = $classifier.Extent.Text + "`n" + '$root = ''' + $root.Replace("'", "''") + '''; $stage = ''initdb''; try { throw ''synthetic-secret-sentinel'' } catch ' + $failureBody + "`nif (`$failed) { exit 1 }"
        [IO.File]::WriteAllText($diagnostic, $source)
        & $pwsh -NoProfile -NonInteractive -File $diagnostic > $out 2> $err
        if ($LASTEXITCODE -ne 1 -or (Get-Item $out).Length -ne 0) { throw 'initdb failure status/stdout changed' }
        $expected = '^native fixture preflight failed \(stage=initdb; line=[0-9]+; initdb=' + [regex]::Escape($case[0]) + '\); no native support evidence established$'
        if ((Get-Content -Raw $err).Trim() -cnotmatch $expected) {
            throw 'initdb public diagnostic classification/leak regression'
        }
    }
    $templates = @(
        @('windows-token', 'could not open process token: error code 5'),
        @('windows-token', 'could not allocate SIDs: error code 5'),
        @('windows-token', 'could not re-execute with restricted token: error code 5'),
        @('windows-token', 'could not get exit code from subprocess: error code 5'),
        @('windows-token', 'could not start process for command ""C:\secret\postgres.exe" -V": error code 5'),
        @('child-exec', 'program "secret" is needed by initdb but was not found in the same directory as "secret"'),
        @('child-exec', 'program "secret" was found by "secret" but was not the same version as initdb'),
        @('file-io', 'could not create directory "secret": Permission denied'),
        @('file-io', 'could not access directory "secret": Permission denied'),
        @('file-io', 'could not change permissions of directory "secret": Permission denied'),
        @('file-io', 'could not change permissions of "secret": Permission denied'),
        @('file-io', 'could not open file "secret" for writing: Permission denied'),
        @('file-io', 'could not access file "secret": Permission denied'),
        @('file-io', 'could not write file "secret": No space left on device'),
        @('file-io', 'could not close file "secret": No space left on device'),
        @('file-io', 'could not read password from file "secret": Permission denied'),
        @('file-io', 'password file "secret" is empty'),
        @('file-io', 'directory "secret" exists but is not empty'),
        @('file-io', 'could not create symbolic link "secret": Permission denied'),
        @('input-config', 'file "secret" does not exist'),
        @('input-config', 'file "secret" is not a regular file'),
        @('input-config', 'input file "secret" does not belong to PostgreSQL 17'),
        @('input-config', 'invalid locale name "secret"'),
        @('input-config', 'invalid locale name "secret" for builtin provider'),
        @('input-config', 'encoding mismatch'),
        @('input-config', 'invalid authentication method "secret" for "host" connections'),
        @('input-config', '"secret" is not a valid server encoding name')
    )
    foreach ($case in $templates) {
        [IO.File]::WriteAllText($log, '')
        [IO.File]::WriteAllText("$log.err", ('initdb.exe: error: ' + $case[1] + "`r`n"), [Text.UTF8Encoding]::new($false))
        if ((Get-InitdbFailureCode $log) -cne $case[0]) { throw 'initdb message template classification failed' }
    }
    $boundary = "initdb: error: encoding mismatch`n"
    $boundary += 'x' * (65536 - $boundary.Length)
    [IO.File]::WriteAllText("$log.err", $boundary, [Text.UTF8Encoding]::new($false))
    if ((Get-InitdbFailureCode $log) -cne 'input-config') { throw 'exact log byte limit rejected' }
    [IO.File]::WriteAllText("$log.err", ($boundary + 'x'), [Text.UTF8Encoding]::new($false))
    if ((Get-InitdbFailureCode $log) -cne 'unknown') { throw 'oversized stderr accepted' }
    [IO.File]::WriteAllText("$log.err", 'initdb: error: encoding mismatch')
    [IO.File]::WriteAllText($log, ('x' * 65537))
    if ((Get-InitdbFailureCode $log) -cne 'unknown') { throw 'oversized stdout accepted' }
    Remove-Item -LiteralPath $log
    if ((Get-InitdbFailureCode $log) -cne 'unknown') { throw 'missing log accepted' }
    $null = New-Item -ItemType Directory -Path $log
    if ((Get-InitdbFailureCode $log) -cne 'unknown') { throw 'unreadable log accepted' }
    Remove-Item -LiteralPath $log
    [IO.File]::WriteAllText($log, '')
    Remove-Item -LiteralPath "$log.err"
    if ((Get-InitdbFailureCode $log) -cne 'unknown') { throw 'missing stderr accepted' }
    [IO.File]::WriteAllText("$log.err", "initdb: error: could not create restricted token: error code 5`nExecution of PostgreSQL by a user with administrative permissions is not`npermitted.")
    if ((Get-InitdbFailureCode $log) -cne 'windows-admin') { throw 'initdb classification priority changed' }
    [IO.File]::WriteAllText("$log.err", 'INITDB: ERROR: encoding mismatch')
    if ((Get-InitdbFailureCode $log) -cne 'unknown') { throw 'nonmatching diagnostic prefix accepted' }

    $ownedRoot = Join-Path $root ('pgsavvy-native.' + [guid]::NewGuid().ToString('N'))
    $null = New-Item -ItemType Directory -Path $ownedRoot
    [IO.File]::WriteAllText((Join-Path $ownedRoot 'init.log'), $secret)
    [IO.File]::WriteAllText((Join-Path $ownedRoot 'init.log.err'), 'initdb: error: encoding mismatch')
    $source = $classifier.Extent.Text + "`n" + '$ErrorActionPreference = ''Stop''; $root = ''' + $ownedRoot.Replace("'", "''") + '''; $owned = $true; $pg = $null; $stage = ''initdb''; try { throw ''synthetic-secret-sentinel'' } catch ' + $failureBody + ' finally ' + $fixtureTry.Finally.Extent.Text + "`nif (`$failed) { exit 1 }"
    [IO.File]::WriteAllText($diagnostic, $source)
    & $pwsh -NoProfile -NonInteractive -File $diagnostic > $out 2> $err
    if ($LASTEXITCODE -ne 1 -or (Get-Item $out).Length -ne 0) { throw 'cleanup failure status/stdout changed' }
    if (Test-Path -LiteralPath $ownedRoot) { throw 'owned diagnostic logs were not cleaned up' }
    if ((Get-Content -Raw $err).Trim() -cnotmatch '^native fixture preflight failed \(stage=initdb; line=[0-9]+; initdb=input-config\); no native support evidence established$') {
        throw 'sanitized initdb classification did not survive owned cleanup'
    }
    Write-Output 'native fixture script regressions passed'
} finally {
    Remove-Item -LiteralPath $root -Recurse -Force
}
