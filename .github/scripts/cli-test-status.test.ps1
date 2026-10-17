[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$tokens = $null
$errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'cli-postgres.ps1'), [ref]$tokens, [ref]$errors)
$helper = $ast.Find({ param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Write-BaselineTestStatus' }, $true)
if (-not $helper) { throw 'safe test status reporter missing' }
. ([scriptblock]::Create($helper.Extent.Text))
$temp = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
$root = Join-Path $temp ('pgsavvy-status-test.' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $root
$expected = @('TestMachineCLI_Dispatch', 'TestMachineCLI_ExitCodes', 'TestMachineCLI_Envelope', 'TestMachineCLI_Envelope_ParentDeadlineAndInput', 'TestMachineCLI_OutputLimit', 'TestMachineCLI_BrokenWriter')
function Get-TestReport([string]$Path) {
    $writer = [IO.StringWriter]::new()
    $savedOut = [Console]::Out
    try {
        [Console]::SetOut($writer)
        $emitted = @(Write-BaselineTestStatus $Path *>&1)
    } finally { [Console]::SetOut($savedOut) }
    $text = $writer.ToString()
    $writer.Dispose()
    if ($emitted.Count -ne 0 -or $text.Contains('synthetic-private') -or $text.Contains($root)) { throw 'private test data was published' }
    $report = $text | ConvertFrom-Json
    if ($report.scope -cne 'native-baseline-test-status' -or $report.tests.Count -ne 6) { throw 'test status inventory changed' }
    if ((@($report.PSObject.Properties.Name) -join ',') -cne 'scope,log_status,tests') { throw 'unexpected public report fields' }
    for ($i = 0; $i -lt 6; $i++) {
        if ($report.tests[$i].test -cne $expected[$i] -or $report.tests[$i].status -cnotin @('passed', 'failed', 'not_completed')) { throw 'unallowlisted test status published' }
        if ((@($report.tests[$i].PSObject.Properties.Name) -join ',') -cne 'test,status') { throw 'unexpected public test fields' }
    }
    return $report
}
try {
    $path = Join-Path $root 'tests.jsonl'
    $lines = @()
    foreach ($name in $expected) { $lines += (@{Action = 'pass'; Test = $name; Output = 'synthetic-private-assertion'; Package = 'synthetic-private-package'} | ConvertTo-Json -Compress) }
    $lines += (@{Action = 'fail'; Test = $expected[0]; Output = 'synthetic-private-password'} | ConvertTo-Json -Compress)
    $lines += (@{Action = 'pass'; Test = $expected[0]} | ConvertTo-Json -Compress)
    $lines += (@{Action = 'fail'; Test = 'synthetic-private-unknown-test'} | ConvertTo-Json -Compress)
    $lines += (@{Action = 'fail'; Test = $expected[1].ToLowerInvariant()} | ConvertTo-Json -Compress)
    [IO.File]::WriteAllText($path, ($lines -join "`n"), [Text.UTF8Encoding]::new($false))
    $report = Get-TestReport $path
    if ($report.log_status -cne 'parsed' -or $report.tests[0].status -cne 'failed' -or @($report.tests | Where-Object status -CEQ 'passed').Count -ne 5) { throw 'pass/fail or case-sensitive aggregation changed' }

    foreach ($case in @('missing', 'invalid', 'partial', 'deep', 'oversized', 'utf8', 'empty')) {
        if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path }
        switch ($case) {
            invalid { [IO.File]::WriteAllText($path, 'synthetic-private-invalid-json') }
            partial { [IO.File]::WriteAllText($path, ($lines[0] + "`nsynthetic-private-invalid-tail")) }
            deep { [IO.File]::WriteAllText($path, (('{"Output":' * 12) + '"synthetic-private-nested"' + ('}' * 12))) }
            oversized { [IO.File]::WriteAllBytes($path, [byte[]]::new(1048577)) }
            utf8 { [IO.File]::WriteAllBytes($path, [byte[]]@(255)) }
            empty { [IO.File]::WriteAllText($path, '') }
        }
        $report = Get-TestReport $path
        $status = switch ($case) { missing { 'unavailable' }; oversized { 'oversized' }; empty { 'parsed' }; default { 'invalid' } }
        if ($report.log_status -cne $status -or @($report.tests | Where-Object status -CNE 'not_completed').Count -ne 0) { throw 'incomplete log did not fail closed' }
    }
    $prefix = '{"Action":"pass","Test":"' + $expected[0] + '","Output":"'
    $boundary = $prefix + ('x' * (1048576 - $prefix.Length - 2)) + '"}'
    [IO.File]::WriteAllText($path, $boundary, [Text.UTF8Encoding]::new($false))
    $report = Get-TestReport $path
    if ($report.log_status -cne 'parsed' -or $report.tests[0].status -cne 'passed') { throw 'exact test-log byte limit rejected' }

    $fixtureTry = @($ast.EndBlock.Statements | Where-Object { $_ -is [Management.Automation.Language.TryStatementAst] })[0]
    [IO.File]::WriteAllText($path, ($lines -join "`n"), [Text.UTF8Encoding]::new($false))
    $pwsh = Join-Path $PSHOME $(if ($IsWindows) { 'pwsh.exe' } else { 'pwsh' })
    $script = Join-Path $root 'catch.ps1'
    $source = '$ErrorActionPreference = ''Stop''; ' + $helper.Extent.Text + "`n" + '$root = ''' + $root.Replace("'", "''") + '''; $stage = ''tests''; try { throw ''synthetic-private-error'' } catch ' + $fixtureTry.CatchClauses[0].Body.Extent.Text + "`nif (`$failed) { exit 1 }"
    [IO.File]::WriteAllText($script, $source, [Text.UTF8Encoding]::new($false))
    $out = Join-Path $root 'public.out'
    $err = Join-Path $root 'public.err'
    & $pwsh -NoProfile -NonInteractive -File $script > $out 2> $err
    if ($LASTEXITCODE -ne 1) { throw 'reporter changed production failure exit' }
    $published = Get-Content -Raw $out
    if ($published.Contains('synthetic-private') -or ($published | ConvertFrom-Json).tests[0].status -cne 'failed') { throw 'production catch lost safe statuses or leaked private text' }
    if ((Get-Content -Raw $err).Trim() -cnotmatch '^native fixture preflight failed \(stage=tests; line=[0-9]+\); no native support evidence established$') { throw 'production catch disclosed unexpected diagnostics' }
    Write-Output 'safe baseline test status regressions passed'
} finally {
    Remove-Item -LiteralPath $root -Recurse -Force
}
