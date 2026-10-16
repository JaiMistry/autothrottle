# Installs, upgrades and uninstalls AutoThrottle silently, the way a user
# would with the wizard, checking each step. Needs an administrator and a
# machine with nothing else on port 7676.
#
#   pwsh packaging/windows/test-install.ps1 -Installer .\AutoThrottle-Setup-1.4.2.exe

param([Parameter(Mandatory)] [string] $Installer)

$ErrorActionPreference = 'Stop'
$Installer = (Resolve-Path $Installer).Path
$app = Join-Path $env:ProgramFiles 'AutoThrottle'
$data = Join-Path $env:ProgramData 'AutoThrottle'
$base = 'http://127.0.0.1:7676'
$startMenu = Join-Path $env:ProgramData 'Microsoft\Windows\Start Menu\Programs'
$logs = Join-Path $PWD 'install-logs'
New-Item -ItemType Directory -Force $logs | Out-Null

function Check([bool] $ok, [string] $what) {
    if (-not $ok) { throw "FAIL  $what" }
    Write-Host "PASS  $what"
}

function Install([string] $label) {
    $p = Start-Process $Installer -Wait -PassThru -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', "/LOG=$logs\$label.log"
    Check ($p.ExitCode -eq 0) "$label exits 0 (got $($p.ExitCode))"
}

function Uninstall([string] $label, [string[]] $extra = @()) {
    $p = Start-Process (Join-Path $app 'unins000.exe') -Wait -PassThru -ArgumentList (@('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', "/LOG=$logs\$label.log") + $extra)
    Check ($p.ExitCode -eq 0) "$label exits 0 (got $($p.ExitCode))"
    # The uninstaller hands over to a copy of itself and returns at once.
    $deadline = (Get-Date).AddSeconds(90)
    while ((Test-Path (Join-Path $app 'unins000.exe')) -or (Get-Service AutoThrottle -ErrorAction SilentlyContinue)) {
        if ((Get-Date) -gt $deadline) { throw "FAIL  $label did not finish within 90s" }
        Start-Sleep -Milliseconds 500
    }
}

# Status code and redirect target, without following it.
function Http([string] $path) {
    $out = & curl.exe -s -o NUL -w '%{http_code} %{redirect_url}' --max-time 5 "$base$path"
    return "$out".Trim()
}

function WaitForHttp([string] $path, [string] $want, [int] $seconds = 60) {
    $deadline = (Get-Date).AddSeconds($seconds)
    do {
        $got = Http $path
        if ($got.StartsWith($want)) { return $got }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    throw "FAIL  GET $path answered '$got', wanted '$want'"
}

function ServiceInfo { Get-CimInstance Win32_Service -Filter "Name='AutoThrottle'" }

function Machine([string] $exe) {
    $bytes = [IO.File]::ReadAllBytes($exe)
    $pe = [BitConverter]::ToInt32($bytes, 0x3c)
    return [BitConverter]::ToUInt16($bytes, $pe + 4)
}

Write-Host "== Fresh install"
Install 'install'
$svc = ServiceInfo
Check ($null -ne $svc) 'the service is registered'
Check ($svc.State -eq 'Running') "the service is running ($($svc.State))"
Check ($svc.StartMode -eq 'Auto') "it starts with Windows ($($svc.StartMode))"
Check ($svc.StartName -eq 'NT AUTHORITY\LocalService') "it runs as LocalService ($($svc.StartName))"

# 12 is Arm64 in Win32_Processor; everything else here is x64.
$arm = (Get-CimInstance Win32_Processor | Select-Object -First 1).Architecture -eq 12
$want = if ($arm) { 0xAA64 } else { 0x8664 }
foreach ($exe in 'autothrottle.exe', 'autothrottle-tray.exe') {
    $got = Machine (Join-Path $app $exe)
    Check ($got -eq $want) ("{0} is the native build for this machine ({1:X4})" -f $exe, $got)
}

WaitForHttp '/setup' '200' | Out-Null
Check $true 'the create-account page answers'
$status = Invoke-RestMethod "$base/local/status"
Check ($status.setup -eq $true) "the tray's status reads not set up yet ($($status.statement))"

$rule = Get-NetFirewallRule -DisplayName 'AutoThrottle' -ErrorAction SilentlyContinue
Check ($null -ne $rule -and $rule.Enabled -eq 'True') 'the firewall lets AutoThrottle in'
$profiles = "$($rule.Profile)"
Check ($profiles -match 'Private' -and $profiles -match 'Domain' -and $profiles -notmatch 'Public') "on private and domain networks only ($profiles)"

$acl = Get-Acl $data
Check $acl.AreAccessRulesProtected 'the data folder does not inherit from ProgramData'
$open = $acl.Access | Where-Object { "$($_.IdentityReference)" -match '^(BUILTIN\\Users|Everyone|NT AUTHORITY\\Authenticated Users)$' }
Check ($null -eq $open) 'other users cannot read the data folder'

$log = Join-Path $data 'autothrottle.log'
Check ((Test-Path $log) -and ((Get-Content $log -Raw) -match 'msg=listening')) 'the service writes its log'
Check (Test-Path (Join-Path $startMenu 'AutoThrottle.lnk')) 'the Start menu has AutoThrottle'
Check (Test-Path (Join-Path $startMenu 'Startup\AutoThrottle.lnk')) 'the tray icon starts at sign-in'
& (Join-Path $app 'autothrottle.exe') help 2>$null
Check ($LASTEXITCODE -eq 2) 'the command line prints its usage'

Write-Host "== Create an account"
$jar = Join-Path $env:TEMP 'autothrottle-cookies.txt'
$page = & curl.exe -s -c $jar "$base/setup"
$token = [regex]::Match("$page", 'name="csrf_token" value="([^"]+)"').Groups[1].Value
Check ($token -ne '') 'the setup page carries a CSRF token'
$posted = & curl.exe -s -o NUL -w '%{http_code}' -b $jar -c $jar --data-urlencode "csrf_token=$token" -d 'username=admin' -d 'password=ci-password-123' "$base/setup"
Check ($posted -eq '303') "the account is created ($posted)"
Check ((Http '/').StartsWith('303 http://127.0.0.1:7676/login')) 'signed out, the dashboard asks for a sign-in'

Write-Host "== The service comes back after a crash"
$before = (ServiceInfo).ProcessId
Stop-Process -Id $before -Force
$deadline = (Get-Date).AddSeconds(60)
while (((ServiceInfo).State -ne 'Running') -or ((ServiceInfo).ProcessId -eq $before)) {
    if ((Get-Date) -gt $deadline) { throw 'FAIL  the service was not restarted after being killed' }
    Start-Sleep -Milliseconds 500
}
WaitForHttp '/login' '200' | Out-Null
Check $true 'Windows restarted it, and it answers again'

Write-Host "== Upgrade in place"
Install 'upgrade'
Check ((ServiceInfo).State -eq 'Running') 'the service is running after the upgrade'
WaitForHttp '/' '303 http://127.0.0.1:7676/login' | Out-Null
Check $true 'the account survived the upgrade'
Check (Test-Path "$log.old") "the previous run's log is kept"
Check (@(Get-NetFirewallRule -DisplayName 'AutoThrottle').Count -eq 1) 'the firewall rule is not duplicated'

Write-Host "== Uninstall, keeping the data"
Uninstall 'uninstall'
Check ($null -eq (ServiceInfo)) 'the service is removed'
Check ($null -eq (Get-NetFirewallRule -DisplayName 'AutoThrottle' -ErrorAction SilentlyContinue)) 'the firewall rule is removed'
Check (-not (Test-Path (Join-Path $startMenu 'AutoThrottle.lnk'))) 'the Start menu entry is removed'
Check (-not (Test-Path (Join-Path $app 'autothrottle.exe'))) 'the program files are removed'
Check (Test-Path (Join-Path $data 'autothrottle.db')) 'settings and history are kept for a reinstall'

Write-Host "== Reinstall picks up the kept data, then uninstall with /PURGE"
Install 'reinstall'
WaitForHttp '/' '303 http://127.0.0.1:7676/login' | Out-Null
Check $true 'the kept account is still there'
Uninstall 'purge' @('/PURGE')
Check (-not (Test-Path $data)) '/PURGE deletes the data too'

Write-Host "All checks passed."
