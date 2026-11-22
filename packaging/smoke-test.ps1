$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$testDir = Join-Path $root 'dist/smoke'
New-Item -ItemType Directory -Force $testDir | Out-Null
$installDir = Join-Path $testDir 'installed'
$installer = Start-Process -FilePath "$root/dist/TempMail-OTP-Hub-Setup.exe" -ArgumentList @('/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART',"/DIR=`"$installDir`"",'/TASKS=') -WindowStyle Hidden -PassThru -Wait
if ($installer.ExitCode -ne 0) { throw "Install failed: $($installer.ExitCode)" }
$processes = @()
$oldPath = $env:PATH
$oldAppData = $env:APPDATA
try {
    $env:PATH = "$env:SystemRoot\System32;$env:SystemRoot"
    $env:APPDATA = Join-Path $testDir 'profile'
    foreach ($binary in @("$root/dist/tempmail-hub.exe", "$installDir/tempmail-hub.exe")) {
        $n = $processes.Count
        $log = Join-Path $testDir "server-$n.log"
        $processes += Start-Process -FilePath $binary -ArgumentList '-no-browser' -WorkingDirectory $testDir -RedirectStandardError $log -WindowStyle Hidden -PassThru
        $url = $null
        for ($i = 0; $i -lt 50; $i++) {
            Start-Sleep -Milliseconds 100
            $content = Get-Content $log -Raw -ErrorAction SilentlyContinue
            if ($content -match 'started at: (http://127\.0\.0\.1:\d+)') { $url = $Matches[1]; break }
        }
        if (!$url) { throw "App failed to start: $content" }
        $page = Invoke-WebRequest $url -UseBasicParsing
        if ($page.StatusCode -ne 200 -or $page.Content -notmatch 'ساخت') { throw 'Dashboard did not render' }
        $accounts = Invoke-WebRequest "$url/api/accounts" -UseBasicParsing
        if ($accounts.StatusCode -ne 200) { throw 'Accounts endpoint failed' }
        Write-Output "PASS: $binary serves dashboard and accounts at $url without development tools on PATH"
    }
    if (!(Test-Path "$env:APPDATA/TempMail-OTP-Hub/accounts_credentials.txt")) { throw 'User data file missing' }
    Write-Output 'PASS: credentials stored in user profile; simultaneous launches work'
} finally {
    foreach ($process in $processes) { if (!$process.HasExited) { Stop-Process -Id $process.Id -Force } }
    $env:PATH = $oldPath
    $env:APPDATA = $oldAppData
    if (Test-Path "$installDir/unins000.exe") {
        $uninstaller = Start-Process -FilePath "$installDir/unins000.exe" -ArgumentList '/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART' -WindowStyle Hidden -PassThru -Wait
        if ($uninstaller.ExitCode -ne 0) { throw 'Uninstall failed' }
        Write-Output 'PASS: uninstall completed'
    }
}
