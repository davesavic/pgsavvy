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

function Invoke-Checked([string]$File, [string[]]$Arguments, [string]$Log) {
    # Keep machine-readable stdout separate from native tool diagnostics.
    & $File @Arguments > $Log 2> "$Log.err"
    if ($LASTEXITCODE -ne 0) { throw 'native fixture command failed' }
}

function Get-InitdbFailureCode([string]$Log) {
    try {
        $texts = @()
        foreach ($path in @($Log, "$Log.err")) {
            $stream = [IO.File]::Open($path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
            try {
                if ($stream.Length -gt 65536) { return 'unknown' }
                $buffer = [byte[]]::new(65536)
                $count = 0
                while ($count -lt $buffer.Length) {
                    $read = $stream.Read($buffer, $count, $buffer.Length - $count)
                    if ($read -eq 0) { break }
                    $count += $read
                }
                if ($stream.Length -gt 65536) { return 'unknown' }
                $texts += [Text.Encoding]::UTF8.GetString($buffer, 0, $count)
            } finally { $stream.Dispose() }
        }
        $timeout = [TimeSpan]::FromMilliseconds(100)
        $options = [Text.RegularExpressions.RegexOptions]::CultureInvariant
        if ([regex]::IsMatch($texts[1], '(?m)^Execution of PostgreSQL by a user with administrative permissions is not\r?\npermitted\.\r?$', $options, $timeout)) {
            return 'windows-admin'
        }
        # PostgreSQL 17/18 message templates only; captures and raw diagnostics
        # never leave the owned directory. Priority is fixed by this table.
        $patterns = [ordered]@{
            'windows-token' = @(
                'could not (?:open process token|allocate SIDs|create restricted token|re-execute with restricted token|get exit code from subprocess): error code [0-9]+',
                'could not start process for command "[^\r\n]*": error code [0-9]+'
            )
            'child-exec' = @(
                'could not execute command "[^\r\n]*": [^\r\n]+',
                'program "[^"\r\n]*" is needed by [^\r\n]+ but was not found in the same directory as "[^"\r\n]*"',
                'program "[^"\r\n]*" was found by "[^"\r\n]*" but was not the same version as [^\r\n]+'
            )
            'file-io' = @(
                'could not (?:create|access) directory "[^"\r\n]*": [^\r\n]+',
                'could not change permissions of (?:directory )?"[^"\r\n]*": [^\r\n]+',
                'could not open file "[^"\r\n]*" for (?:reading|writing): [^\r\n]+',
                'could not (?:access|write|close) file "[^"\r\n]*": [^\r\n]+',
                'could not read password from file "[^"\r\n]*": [^\r\n]+',
                'password file "[^"\r\n]*" is empty',
                'directory "[^"\r\n]*" exists but is not empty',
                'could not create symbolic link "[^"\r\n]*": [^\r\n]+'
            )
            'input-config' = @(
                'file "[^"\r\n]*" (?:does not exist|is not a regular file)',
                'input file "[^"\r\n]*" does not belong to PostgreSQL [^\r\n]+',
                'invalid locale settings; check LANG and LC_\* environment variables',
                'invalid locale name "[^"\r\n]*"(?: for builtin provider)?',
                'encoding mismatch',
                'invalid authentication method "[^"\r\n]*" for "[^"\r\n]*" connections',
                '"[^"\r\n]*" is not a valid server encoding name'
            )
        }
        foreach ($entry in $patterns.GetEnumerator()) {
            $pattern = '(?m)^initdb(?:\.exe)?: error: (?:' + ($entry.Value -join '|') + ')\r?$'
            if ([regex]::IsMatch($texts[1], $pattern, $options, $timeout)) { return $entry.Key }
        }
        if ([regex]::IsMatch($texts[0], '(?m)^(?:running bootstrap script \.\.\. )?could not write to child process: [^\r\n]+\r?$', $options, $timeout)) {
            return 'child-io'
        }
    } catch { return 'unknown' }
    return 'unknown'
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
    $password = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)).ToLowerInvariant()
    $passwordFile = Join-Path $root 'password'
    [IO.File]::WriteAllText($passwordFile, "$password`n", [Text.UTF8Encoding]::new($false))
    $data = Join-Path $root 'data'
    $stage = 'initdb'
    Invoke-Checked (Join-Path $pg 'initdb.exe') @('-D', $data, '-U', 'pgsavvy', '--encoding=UTF8', '--no-locale', '--auth-local=scram-sha-256', '--auth-host=scram-sha-256', "--pwfile=$passwordFile") (Join-Path $root 'init.log')
    $stage = 'startup'
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = $listener.LocalEndpoint.Port
    $listener.Stop()
    Add-Content (Join-Path $data 'postgresql.conf') "`nlisten_addresses = '127.0.0.1'`nport = $port`nunix_socket_directories = ''"
    Invoke-Checked (Join-Path $pg 'pg_ctl.exe') @('-D', $data, '-l', (Join-Path $root 'server.log'), '-w', '-t', '30', 'start') (Join-Path $root 'start.log')
    $env:PGPASSWORD = $password
    $stage = 'createdb'
    Invoke-Checked (Join-Path $pg 'createdb.exe') @('-h', '127.0.0.1', '-p', "$port", '-U', 'pgsavvy', 'pgsavvy_test') (Join-Path $root 'createdb.log')
    $psql = Join-Path $pg 'psql.exe'
    $connection = @('-X', '-w', '-h', '127.0.0.1', '-p', "$port", '-U', 'pgsavvy', '-d', 'pgsavvy_test', '-v', 'ON_ERROR_STOP=1')
    $stage = 'fixture'
    Invoke-Checked $psql ($connection + @('-f', (Join-Path $repo 'docker/postgres/init/01_fixture.sql'))) (Join-Path $root 'fixture.log')
    Invoke-Checked $psql ($connection + @('-Atc', 'SELECT 1')) (Join-Path $root 'ready')
    if ((Get-Content -Raw (Join-Path $root 'ready')).Trim() -ne '1') { throw 'fixture not ready' }
    $stage = 'authentication'
    $env:PGPASSWORD = 'deliberately-wrong'
    & $psql @connection -Atc 'SELECT 1' > (Join-Path $root 'wrong.out') 2> (Join-Path $root 'wrong.err')
    $wrongExit = $LASTEXITCODE
    $env:PGPASSWORD = $password
    if ($wrongExit -eq 0 -or -not (Select-String -Quiet -SimpleMatch 'password authentication failed' (Join-Path $root 'wrong.err'))) {
        throw 'wrong-password rejection not proven'
    }
    $stage = 'facts'
    $sql = "SELECT current_setting('server_version_num'), current_setting('server_encoding'), (SELECT version FROM app._fixture_meta), rolsuper FROM pg_roles WHERE rolname = current_user"
    Invoke-Checked $psql ($connection + @('-Atc', $sql)) (Join-Path $root 'facts')
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
    # Do not publish raw tool errors; they can include environment/connection text.
    $diagnostic = 'stage=' + $stage + '; line=' + [int]$_.InvocationInfo.ScriptLineNumber
    if ($stage -ceq 'initdb') {
        $diagnostic += '; initdb=' + (Get-InitdbFailureCode (Join-Path $root 'init.log'))
    }
    [Console]::Error.WriteLine('native fixture preflight failed (' + $diagnostic + '); no native support evidence established')
} finally {
    if ($owned) {
        try {
            $stopped = $true
            if (Test-Path (Join-Path $root 'data/postmaster.pid')) {
                & (Join-Path $pg 'pg_ctl.exe') -D (Join-Path $root 'data') -m immediate -w -t 30 stop > (Join-Path $root 'stop.log') 2>&1
                $stopped = $LASTEXITCODE -eq 0
            }
            if (-not $stopped) { throw 'owned cluster stop failed' }
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
