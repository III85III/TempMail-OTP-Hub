param([string]$Version = '1.0.0')
$ErrorActionPreference = 'Stop'
Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    New-Item -ItemType Directory -Force dist | Out-Null
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    go build -trimpath -ldflags '-s -w' -o dist/tempmail-hub.exe .
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }
    $compiler = Get-Command ISCC.exe -ErrorAction SilentlyContinue
    $compilerPath = if ($compiler) { $compiler.Source } else { "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe" }
    if (!(Test-Path $compilerPath)) { $compilerPath = "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe" }
    if (!(Test-Path $compilerPath)) { throw 'Install Inno Setup 6 on the build machine.' }
    & $compilerPath /Qp "/DAppVersion=$Version" packaging/windows.iss
    if ($LASTEXITCODE -ne 0) { throw 'Installer build failed' }
    Compress-Archive -Path dist/tempmail-hub.exe,run_pool_gen.bat,LICENSE -DestinationPath dist/TempMail-OTP-Hub-Windows-x64.zip -Force
    Get-ChildItem dist -File | Where-Object { $_.Extension -in '.exe','.zip' } | ForEach-Object {
        '{0}  {1}' -f (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower(), $_.Name
    } | Set-Content dist/SHA256SUMS.txt -Encoding ascii
} finally {
    Pop-Location
}
