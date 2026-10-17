# Native baseline only. Full machine acceptance is owned by pgsavvy-y2rh.14.
[CmdletBinding()]
param([Parameter(Mandatory)][ValidateSet('17', '18')][string]$Major)
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$repo = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$root = $null
$owned = $false
$pg = $null
$failed = $false
$stage = 'toolchain'
$probeCleanupFailed = $false
$controllerCleanupFailed = $false

function Invoke-Checked([string]$File, [string[]]$Arguments, [string]$Log) {
    # Keep machine-readable stdout separate from native tool diagnostics.
    $script:lastNativeExit = $null
    & $File @Arguments > $Log 2> "$Log.err"
    $script:lastNativeExit = $LASTEXITCODE
    if ($script:lastNativeExit -ne 0) { throw 'native fixture command failed' }
}

function Invoke-IsolatedChecked([string]$File, [string[]]$Arguments, [hashtable]$Environment, [string]$Directory, [ref]$CleanupFailed, [switch]$AllowNonZeroExit) {
    $result = Invoke-ProbeCommand $File $Arguments $Environment $Directory
    $script:lastNativeExit = $result.exit_code
    if ($result.cleanup_failed) { $CleanupFailed.Value = $true }
    if (-not $result.started -or $result.exit_code -eq 125 -or (-not $AllowNonZeroExit -and $result.exit_code -ne 0) -or $result.timed_out -or $result.capture_failed -or $result.cleanup_failed) {
        throw 'isolated native fixture command failed'
    }
}

function Write-BaselineTestStatus([string]$Path) {
    $names = @('TestMachineCLI_Dispatch', 'TestMachineCLI_ExitCodes', 'TestMachineCLI_Envelope', 'TestMachineCLI_Envelope_ParentDeadlineAndInput', 'TestMachineCLI_OutputLimit', 'TestMachineCLI_BrokenWriter')
    $statuses = @{}
    foreach ($name in $names) { $statuses[$name] = 'not_completed' }
    $logStatus = 'unavailable'
    $stream = $null
    try {
        $stream = [IO.File]::Open($Path, 'Open', 'Read', 'Read')
        $logStatus = 'invalid'
        $limit = 1048576
        if ($stream.Length -gt $limit) {
            $logStatus = 'oversized'
        } else {
            $buffer = [byte[]]::new($limit + 1)
            $count = 0
            while ($count -lt $buffer.Length) {
                $read = $stream.Read($buffer, $count, $buffer.Length - $count)
                if ($read -eq 0) { break }
                $count += $read
            }
            if ($count -gt $limit) {
                $logStatus = 'oversized'
            } else {
                $text = [Text.UTF8Encoding]::new($false, $true).GetString($buffer, 0, $count)
                foreach ($line in $text.Split("`n")) {
                    if ([string]::IsNullOrWhiteSpace($line)) { continue }
                    $event = $line | ConvertFrom-Json -Depth 8
                    if ($event.Test -isnot [string] -or $event.Action -isnot [string] -or $names -cnotcontains $event.Test) { continue }
                    if ($event.Action -ceq 'fail') { $statuses[$event.Test] = 'failed' }
                    elseif ($event.Action -ceq 'pass' -and $statuses[$event.Test] -cne 'failed') { $statuses[$event.Test] = 'passed' }
                }
                $logStatus = 'parsed'
            }
        }
    } catch {
        # Never publish parse errors or any fields sourced from private output.
    } finally {
        if ($stream) { $stream.Dispose() }
    }
    if ($logStatus -cne 'parsed') {
        foreach ($name in $names) { $statuses[$name] = 'not_completed' }
    }
    $tests = @($names | ForEach-Object { [ordered]@{ test = $_; status = $statuses[$_] } })
    $report = [ordered]@{ scope = 'native-baseline-test-status'; log_status = $logStatus; tests = $tests }
    [Console]::Out.WriteLine(($report | ConvertTo-Json -Depth 4 -Compress))
}

try {
    if (-not $IsWindows) { throw 'native Windows required' }
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
    if ($arch -eq 'x64') { $arch = 'amd64' }
    if ($arch -notin @('amd64', 'arm64')) { throw 'unsupported host architecture' }
    foreach ($key in @('GOOS', 'GOHOSTOS')) {
        $value = & go env $key 2> $null
        if ($LASTEXITCODE -ne 0 -or $value -ne 'windows') { throw 'non-native Go toolchain' }
    }
    foreach ($key in @('GOARCH', 'GOHOSTARCH')) {
        $value = & go env $key 2> $null
        if ($LASTEXITCODE -ne 0 -or $value -ne $arch) { throw 'non-native Go toolchain' }
    }
    $goVersion = & go env GOVERSION 2> $null
    if ($LASTEXITCODE -ne 0 -or $goVersion -ne 'go1.26.9') { throw 'Go 1.26.9 required' }
    if ($env:PGSAVVY_EXPECT_GOOS -and $env:PGSAVVY_EXPECT_GOOS -ne 'windows') { throw 'wrong runner OS' }
    if ($env:PGSAVVY_EXPECT_GOARCH -and $env:PGSAVVY_EXPECT_GOARCH -ne $arch) { throw 'wrong runner architecture' }

    $stage = 'directory'
    $temp = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
    $root = Join-Path $temp ('pgsavvy-native.' + [guid]::NewGuid().ToString('N'))
    $null = New-Item -ItemType Directory -Path $root
    $owned = $true
    $stage = 'acl'
    $user = [Security.Principal.WindowsIdentity]::GetCurrent().User
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetOwner($user)
    $acl.SetAccessRuleProtection($true, $false)
    $inherit = [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
    $rule = [Security.AccessControl.FileSystemAccessRule]::new($user, 'FullControl', $inherit, 'None', 'Allow')
    $acl.AddAccessRule($rule)
    Set-Acl -LiteralPath $root -AclObject $acl
    $stage = 'manifest'
    $manifest = Get-Content -Raw (Join-Path $PSScriptRoot 'cli-postgres-versions.json') | ConvertFrom-Json
    if ($manifest.schema_version -ne 1) { throw 'invalid artifact manifest' }
    $entry = $manifest.postgres.$Major
    $pin = $entry.windows
    if ($entry.version -notmatch ('^' + $Major + '\.\d+$') -or $pin.sha256 -cnotmatch '^[0-9a-f]{64}$' -or $pin.architecture -ne 'x64') {
        throw 'invalid artifact pin'
    }
    $expectedUrl = 'https://get.enterprisedb.com/postgresql/postgresql-' + $entry.version + '-' + $pin.revision + '-windows-x64-binaries.zip'
    if ($pin.url -cne $expectedUrl) { throw 'unapproved artifact URL' }
    $archive = Join-Path $root 'postgres.zip'
    $stage = 'download'
    Invoke-WebRequest -Uri $pin.url -OutFile $archive -TimeoutSec 600 -MaximumRedirection 0
    $stage = 'digest'
    if ((Get-FileHash -Algorithm SHA256 $archive).Hash.ToLowerInvariant() -cne $pin.sha256) { throw 'PostgreSQL artifact digest mismatch' }
    $stage = 'extraction'
    Expand-Archive -LiteralPath $archive -DestinationPath $root
    $stage = 'tools-version'
    $pg = Join-Path $root 'pgsql/bin'
    $toolsVersion = & (Join-Path $pg 'psql.exe') --version 2> (Join-Path $root 'tools-version.err')
    if ($LASTEXITCODE -ne 0 -or $toolsVersion -cne ('psql (PostgreSQL) ' + $entry.version)) { throw 'unexpected PostgreSQL tools' }

    $stage = 'environment'
    foreach ($name in @('PGHOST', 'PGHOSTADDR', 'PGPORT', 'PGDATABASE', 'PGUSER', 'PGPASSWORD', 'PGSERVICE', 'PGOPTIONS')) {
        [Environment]::SetEnvironmentVariable($name, $null, 'Process')
    }
    $env:PGSERVICEFILE = Join-Path $root 'no-services'
    $env:PGPASSFILE = Join-Path $root 'no-pgpass'
    $env:PGCONNECT_TIMEOUT = '5'
    $env:PGSSLMODE = 'disable'
    $env:XDG_CONFIG_HOME = Join-Path $root 'config'
    $env:XDG_DATA_HOME = Join-Path $root 'share'
    $env:XDG_STATE_HOME = Join-Path $root 'state'
    $env:XDG_CACHE_HOME = Join-Path $root 'cache'
    $stage = 'initdb-probe'
    . (Join-Path $PSScriptRoot 'cli-initdb-probe.ps1')
    $windowsDirectory = [Environment]::GetFolderPath([Environment+SpecialFolder]::Windows)
    if (-not (Invoke-InitdbProbe $pg $root $repo $windowsDirectory ([ref]$probeCleanupFailed))) { throw 'secret-free initdb probe failed' }
    $password = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).ToLowerInvariant()
    $passwordFile = Join-Path $root 'password'
    [IO.File]::WriteAllText($passwordFile, "$password`n", [Text.UTF8Encoding]::new($false))
    $data = Join-Path $root 'data'
    $stage = 'initdb'
    # Use the launch context verified by the probe, but never publish the real
    # initialization's captured output or substitute its random credential.
    $initdbContext = Join-Path $root 'initdb-context'
    $null = New-Item -ItemType Directory -Path $initdbContext
    $initdbEnvironment = Get-ProbeEnvironment $pg $initdbContext $windowsDirectory
    Invoke-IsolatedChecked (Join-Path $pg 'initdb.exe') @('-D', $data, '-U', 'pgsavvy', '--encoding=UTF8', '--no-locale', '--auth-local=scram-sha-256', '--auth-host=scram-sha-256', "--pwfile=$passwordFile") $initdbEnvironment $initdbContext ([ref]$probeCleanupFailed)
    $stage = 'controller-build'
    $controller = Join-Path $root 'pgctl-runner.exe'
    Push-Location $repo
    try {
        Invoke-Checked 'go' @('build', '-o', $controller, './internal/pgfixture') (Join-Path $root 'controller-build.log')
    } finally { Pop-Location }
    $stage = 'startup'
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = $listener.LocalEndpoint.Port
    $listener.Stop()
    Add-Content (Join-Path $data 'postgresql.conf') "`nlisten_addresses = '127.0.0.1'`nport = $port`nunix_socket_directories = ''"
    Invoke-IsolatedChecked $controller (@((Join-Path $pg 'pg_ctl.exe'), (Join-Path $root 'start.log')) + @('-D', $data, '-l', (Join-Path $root 'server.log'), '-w', '-t', '30', 'start')) $initdbEnvironment $initdbContext ([ref]$controllerCleanupFailed)
    $clientEnvironment = $initdbEnvironment.Clone()
    $clientEnvironment.PGPASSWORD = $password
    $clientEnvironment.PGCONNECT_TIMEOUT = '5'
    $clientEnvironment.PGSSLMODE = 'disable'
    $stage = 'createdb'
    Invoke-IsolatedChecked $controller (@((Join-Path $pg 'createdb.exe'), (Join-Path $root 'createdb.log')) + @('-h', '127.0.0.1', '-p', "$port", '-U', 'pgsavvy', 'pgsavvy_test')) $clientEnvironment $initdbContext ([ref]$controllerCleanupFailed)
    $psql = Join-Path $pg 'psql.exe'
    $connection = @('-X', '-w', '-h', '127.0.0.1', '-p', "$port", '-U', 'pgsavvy', '-d', 'pgsavvy_test', '-v', 'ON_ERROR_STOP=1')
    $stage = 'fixture'
    Invoke-IsolatedChecked $controller (@($psql, (Join-Path $root 'fixture.log')) + $connection + @('-f', (Join-Path $repo 'docker/postgres/init/01_fixture.sql'))) $clientEnvironment $initdbContext ([ref]$controllerCleanupFailed)
    Invoke-IsolatedChecked $controller (@($psql, (Join-Path $root 'ready')) + $connection + @('-Atc', 'SELECT 1')) $clientEnvironment $initdbContext ([ref]$controllerCleanupFailed)
    if ((Get-Content -Raw (Join-Path $root 'ready')).Trim() -ne '1') { throw 'fixture not ready' }
    $stage = 'authentication'
    $wrongEnvironment = $clientEnvironment.Clone()
    $wrongEnvironment.PGPASSWORD = 'deliberately-wrong'
    Invoke-IsolatedChecked $controller (@($psql, (Join-Path $root 'wrong.out')) + $connection + @('-Atc', 'SELECT 1')) $wrongEnvironment $initdbContext ([ref]$controllerCleanupFailed) -AllowNonZeroExit
    $wrongExit = $script:lastNativeExit
    if ($wrongExit -ne 2 -or -not (Select-String -Quiet -SimpleMatch 'password authentication failed' (Join-Path $root 'wrong.out.err'))) {
        throw 'wrong-password rejection not proven'
    }
    $stage = 'facts'
    $sql = "SELECT current_setting('server_version_num'), current_setting('server_encoding'), (SELECT version FROM app._fixture_meta), rolsuper FROM pg_roles WHERE rolname = current_user"
    Invoke-IsolatedChecked $controller (@($psql, (Join-Path $root 'facts')) + $connection + @('-Atc', $sql)) $clientEnvironment $initdbContext ([ref]$controllerCleanupFailed)
    $facts = (Get-Content -Raw (Join-Path $root 'facts')).Trim().Split('|')
    if ($facts.Count -ne 4 -or [math]::Floor([int]$facts[0] / 10000) -ne [int]$Major -or $facts[1] -ne 'UTF8' -or $facts[2] -ne '2' -or $facts[3] -ne 't') {
        throw 'fixture version/auth/encoding mismatch'
    }
    $env:PGSAVVY_TEST_PG = "postgres://pgsavvy:${password}@127.0.0.1:$port/pgsavvy_test?sslmode=disable"
    $env:CGO_ENABLED = '0'
    Push-Location $repo
    try {
        $stage = 'probe'
        Invoke-Checked 'go' @('run', './internal/pgprobe') (Join-Path $root 'probe.log')
        $stage = 'build'
        $binary = Join-Path $root 'pgsavvy.exe'
        Invoke-Checked 'go' @('build', '-ldflags', '-X main.version=native-preflight -X main.buildSource=fixture', '-o', $binary, '.') (Join-Path $root 'go-build.log')
        $env:PGSAVVY_TEST_BINARY = $binary
        $stage = 'binary-version'
        Invoke-Checked $binary @('--version') (Join-Path $root 'version')
        if (-not (Select-String -Quiet -SimpleMatch 'native-preflight' (Join-Path $root 'version'))) { throw 'legacy version route failed' }
        $selector = '^TestMachineCLI_(Dispatch|ExitCodes|Envelope|Envelope_ParentDeadlineAndInput|OutputLimit|BrokenWriter)$'
        $stage = 'tests'
        Invoke-Checked 'go' @('test', '-json', '-count=1', '-timeout', '5m', './pkg/cli', '-run', $selector) (Join-Path $root 'tests.jsonl')
    } finally { Pop-Location }
    $stage = 'evidence'
    $events = @(Get-Content (Join-Path $root 'tests.jsonl') | ForEach-Object { $_ | ConvertFrom-Json })
    if (@($events | Where-Object { $_.Action -in @('skip', 'fail') }).Count -ne 0) { throw 'baseline contains skipped/failed tests' }
    $passed = @($events | Where-Object { $_.Action -eq 'pass' -and $_.Test } | ForEach-Object { $_.Test })
    foreach ($name in @('Dispatch', 'ExitCodes', 'Envelope', 'Envelope_ParentDeadlineAndInput', 'OutputLimit', 'BrokenWriter')) {
        if ('TestMachineCLI_' + $name -cnotin $passed) { throw 'required baseline tests missing' }
    }
    [ordered]@{
        scope = 'baseline-not-native-support'; goos = 'windows'; goarch = $arch
        go_version = $goVersion; cgo_enabled = '0'; binary = $binary
        binary_sha256 = (Get-FileHash -Algorithm SHA256 $binary).Hash.ToLowerInvariant()
        postgres_tools = $entry.version; postgres_tools_arch = 'x64'; server_version_num = $facts[0]
        fixture_version = 2; wrong_password_rejected = $true; tests_passed = @($passed | Sort-Object)
    } | ConvertTo-Json -Compress
} catch {
    $failed = $true
    if ($stage -ceq 'tests' -and $root) { Write-BaselineTestStatus (Join-Path $root 'tests.jsonl') }
    # Do not publish raw tool errors; they can include environment/connection text.
    $diagnostic = 'stage=' + $stage + '; line=' + [int]$_.InvocationInfo.ScriptLineNumber
    if ($stage -ceq 'initdb') {
        $diagnostic += '; native_exit=' + $script:lastNativeExit
    }
    [Console]::Error.WriteLine('native fixture preflight failed (' + $diagnostic + '); no native support evidence established')
} finally {
    if ($owned) {
        try {
            if ($probeCleanupFailed) { throw 'probe process cleanup incomplete' }
            if (Test-Path (Join-Path $root 'data/postmaster.pid')) {
                Invoke-IsolatedChecked $controller (@((Join-Path $pg 'pg_ctl.exe'), (Join-Path $root 'stop.log')) + @('-D', (Join-Path $root 'data'), '-m', 'immediate', '-w', '-t', '30', 'stop')) $initdbEnvironment $initdbContext ([ref]$controllerCleanupFailed)
            }
            if ($controllerCleanupFailed -or (Test-Path (Join-Path $root 'data/postmaster.pid'))) { throw 'owned controller cleanup incomplete' }
            $item = Get-Item -LiteralPath $root
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -or -not $item.Name.StartsWith('pgsavvy-native.')) {
                throw 'refusing unexpected cleanup target'
            }
            Remove-Item -LiteralPath $root -Recurse -Force
        } catch {
            $failed = $true
            [Console]::Error.WriteLine('owned fixture cleanup failed; retained owned fixture')
        }
    }
}
if ($failed) { exit 1 }
