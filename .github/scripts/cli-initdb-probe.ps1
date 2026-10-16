function Invoke-ProbeCommand([string]$File, [string[]]$Arguments, [hashtable]$Environment, [string]$Directory, [int]$TimeoutMS = 120000) {
    $result = [pscustomobject]@{ started = $false; exit_code = $null; launch_error = $null; capture_error = $null; timed_out = $false; capture_failed = $false; cleanup_failed = $false; streams = @() }
    $process = [Diagnostics.Process]::new()
    $readers = @()
    try {
        $info = [Diagnostics.ProcessStartInfo]::new()
        $info.FileName = $File
        $info.WorkingDirectory = $Directory
        $info.UseShellExecute = $false
        $info.RedirectStandardInput = $true
        $info.RedirectStandardOutput = $true
        $info.RedirectStandardError = $true
        $info.StandardOutputEncoding = [Text.UTF8Encoding]::new($false, $true)
        $info.StandardErrorEncoding = [Text.UTF8Encoding]::new($false, $true)
        $info.Environment.Clear()
        foreach ($entry in $Environment.GetEnumerator()) { $info.Environment[$entry.Key] = $entry.Value }
        foreach ($argument in $Arguments) { $info.ArgumentList.Add($argument) }
        $process.StartInfo = $info
        $result.started = $process.Start()
        $process.StandardInput.Close()
        foreach ($name in @('stdout', 'stderr')) {
            $reader = if ($name -eq 'stdout') { $process.StandardOutput } else { $process.StandardError }
            $buffer = [char[]]::new(65536)
            $readers += [pscustomobject]@{ name = $name; reader = $reader; buffer = $buffer; read = $reader.ReadBlockAsync($buffer, 0, $buffer.Length); drain = $null }
        }
        $clock = [Diagnostics.Stopwatch]::StartNew()
        while ($true) {
            foreach ($state in $readers) {
                if ($state.read.IsCompleted -and $null -eq $state.drain) {
                    $null = $state.read.GetAwaiter().GetResult()
                    # Drain excess output without retaining it or blocking the child.
                    $state.drain = $state.reader.BaseStream.CopyToAsync([IO.Stream]::Null)
                }
            }
            if ($process.WaitForExit(20)) { break }
            if ($clock.ElapsedMilliseconds -ge $TimeoutMS) {
                $result.timed_out = $true
                $process.Kill($true)
                if (-not $process.WaitForExit(10000)) { throw 'probe process did not stop' }
                break
            }
        }
        $result.exit_code = $process.ExitCode
        foreach ($state in $readers) {
            if (-not $state.read.Wait(5000)) { throw 'probe capture did not join' }
            $count = $state.read.GetAwaiter().GetResult()
            if ($null -eq $state.drain) { $state.drain = $state.reader.BaseStream.CopyToAsync([IO.Stream]::Null) }
            if (-not $state.drain.Wait(5000)) { throw 'probe drain did not join' }
            $result.streams += [pscustomobject]@{ name = $state.name; text = [string]::new($state.buffer, 0, $count); truncated = $count -eq $state.buffer.Length; captured_chars = $count }
        }
    } catch {
        $result.capture_failed = $result.started
        # An unjoined pipe can belong to a surviving bootstrap descendant.
        # Retain its data rather than assume that parent exit joined the tree.
        $result.cleanup_failed = $result.started
        $errorObject = $_.Exception
        while ($errorObject.InnerException) { $errorObject = $errorObject.InnerException }
        $errorCode = if ($errorObject -is [ComponentModel.Win32Exception]) { $errorObject.NativeErrorCode } else { $errorObject.HResult }
        if ($result.started) { $result.capture_error = $errorCode } else { $result.launch_error = $errorCode }
    } finally {
        try {
            if ($result.started -and -not $process.HasExited) {
                $process.Kill($true)
                if (-not $process.WaitForExit(10000)) { $result.cleanup_failed = $true }
            }
        } catch { $result.cleanup_failed = $true }
        foreach ($state in $readers) {
            try { $state.reader.Dispose() } catch { $result.cleanup_failed = $true }
        }
        try { $process.Dispose() } catch { $result.cleanup_failed = $true }
    }
    return $result
}

function Protect-ProbeText([string]$Text, [bool]$Truncated, [string[]]$Sensitive) {
    # A capture boundary must not leave half a private path/name unredacted.
    if ($Truncated) {
        $end = $Text.LastIndexOf("`n")
        $Text = if ($end -lt 0) { '' } else { $Text.Substring(0, $end + 1) }
    }
    foreach ($value in @($Sensitive | Where-Object { $_ } | Sort-Object Length -Descending | Select-Object -Unique)) {
        foreach ($variant in @(@($value, $value.Replace('\', '/'), $value.Replace('/', '\')) | Select-Object -Unique)) {
            $Text = [regex]::Replace($Text, [regex]::Escape($variant), '***', [Text.RegularExpressions.RegexOptions]'IgnoreCase, CultureInvariant', [TimeSpan]::FromMilliseconds(100))
        }
    }
    # Redact first, then bound publication. JSON encoding escapes control bytes.
    if ($Text.Length -gt 4096) { $Text = $Text.Substring(0, 4096); $Truncated = $true }
    return [pscustomobject]@{ text = $Text; truncated = $Truncated }
}

function Get-ProbeEnvironment([string]$Pg, [string]$Probe, [string]$WindowsDirectory) {
    $system32 = Join-Path $WindowsDirectory 'System32'
    $profile = Join-Path $Probe 'profile'
    $temp = Join-Path $Probe 'temp'
    $null = New-Item -ItemType Directory -Path $profile, $temp
    return @{
        SystemRoot = $WindowsDirectory; WINDIR = $WindowsDirectory
        ComSpec = (Join-Path $system32 'cmd.exe'); PATH = ($Pg + ';' + $system32)
        HOME = $profile; USERPROFILE = $profile; APPDATA = $profile; LOCALAPPDATA = $profile
        TEMP = $temp; TMP = $temp; LANG = 'C'; LC_ALL = 'C'
        PGSYSCONFDIR = $profile; PGSERVICEFILE = (Join-Path $profile 'no-services'); PGPASSFILE = (Join-Path $profile 'no-pgpass')
    }
}

function Invoke-InitdbProbe([string]$Pg, [string]$Root, [string]$Repo, [string]$WindowsDirectory, [ref]$CleanupFailed) {
    $probe = Join-Path $Root 'initdb-probe'
    $null = New-Item -ItemType Directory -Path $probe
    try {
        $environment = Get-ProbeEnvironment $Pg $probe $WindowsDirectory
        $sensitive = @($Root, $Repo, $env:USERPROFILE, $env:HOME, $env:TEMP, $env:TMP, $env:RUNNER_TEMP, $env:USERDOMAIN, [Environment]::UserName, [Environment]::UserDomainName, [Environment]::MachineName)
        $passwordFile = Join-Path $probe 'public-password'
        # Public synthetic input, never a real fixture credential. No TCP server
        # is started; SCRAM/pwfile arguments still match the real initdb path.
        [IO.File]::WriteAllText($passwordFile, "pgsavvy-public-diagnostic-only`n", [Text.UTF8Encoding]::new($false))
        $commands = @(
            @{ name = 'initdb-version'; file = (Join-Path $Pg 'initdb.exe'); arguments = @('--version') },
            @{ name = 'postgres-version'; file = (Join-Path $Pg 'postgres.exe'); arguments = @('--version') },
            @{ name = 'initialize'; file = (Join-Path $Pg 'initdb.exe'); arguments = @('-D', (Join-Path $probe 'data'), '-U', 'pgsavvy', '--encoding=UTF8', '--no-locale', '--auth-local=scram-sha-256', '--auth-host=scram-sha-256', "--pwfile=$passwordFile") }
        )
        $success = $true
        foreach ($command in $commands) {
            $result = Invoke-ProbeCommand $command.file $command.arguments $environment $probe
            if ($result.cleanup_failed) { $CleanupFailed.Value = $true }
            $streams = @()
            foreach ($stream in $result.streams) {
                $protected = Protect-ProbeText $stream.text $stream.truncated $sensitive
                $streams += [pscustomobject]@{ name = $stream.name; text = $protected.text; truncated = $protected.truncated; captured_chars = $stream.captured_chars }
            }
            $public = [ordered]@{
                scope = 'secret-free-initdb-probe'; command = $command.name
                powershell = $PSVersionTable.PSVersion.ToString()
                os_arch = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
                process_arch = [Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString()
                started = $result.started; exit_code = $result.exit_code
                exit_hex = $(if ($null -ne $result.exit_code) { ([int]$result.exit_code).ToString('X8') } else { $null })
                launch_error = $result.launch_error; timed_out = $result.timed_out
                capture_failed = $result.capture_failed; capture_error = $result.capture_error; cleanup_failed = $result.cleanup_failed
                output = $streams
            }
            # Legacy runner commands (##[...]) are recognized inside a line,
            # even inside JSON. Escape '#' without changing the decoded text.
            $json = ($public | ConvertTo-Json -Depth 5 -Compress -EscapeHandling EscapeNonAscii).Replace('#', '\u0023')
            [Console]::Out.WriteLine($json)
            if (-not $result.started -or $result.exit_code -ne 0 -or $result.timed_out -or $result.capture_failed -or $result.cleanup_failed) { $success = $false }
            if ($result.cleanup_failed) { break }
        }
        return $success
    } finally {
        if (-not $CleanupFailed.Value) {
            $item = Get-Item -LiteralPath $probe
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'refusing unexpected probe cleanup target' }
            Remove-Item -LiteralPath $probe -Recurse -Force
        }
    }
}
