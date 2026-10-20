# Installs orOS Drive with the real setup, checks what it put where,
# starts the app, installs again over the running app (an upgrade),
# then uninstalls and checks nothing is left. Runs in CI on Windows.
param([Parameter(Mandatory)][string]$Setup)
$ErrorActionPreference = 'Stop'
$env:OROS_DRIVE_NO_KEYRING = '1'
$app = Join-Path $env:LOCALAPPDATA 'Programs\orOS Drive'
$lnk = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\orOS Drive.lnk'
$cfg = Join-Path $env:APPDATA 'orOS Drive'
$run = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'

function Install {
  $p = Start-Process $Setup -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', '/TASKS=autostart', "/LOG=$env:RUNNER_TEMP\setup.log" -Wait -PassThru
  if ($p.ExitCode) { Get-Content "$env:RUNNER_TEMP\setup.log"; throw "setup exit code $($p.ExitCode)" }
}
function Fail($why) { throw $why }

Install
foreach ($f in 'orOS Drive.exe', 'orosdrive.exe', 'LICENSE.txt', 'THIRD-PARTY-NOTICES.txt', 'oros-drive.ico', 'unins000.exe') {
  if (-not (Test-Path (Join-Path $app $f))) { Fail "missing $f" }
}
if (-not (Test-Path $lnk)) { Fail 'no Start menu shortcut' }
$want = "`"$app\orOS Drive.exe`" run --background"
$got = (Get-ItemProperty $run).'orOS Drive'
if ($got -ne $want) { Fail "Run value: $got" }
$ver = & (Join-Path $app 'orosdrive.exe') version
Write-Host "installed: $ver"

# The app running (as at login), then the setup again: it must stop
# the app, replace the files and succeed.
Start-Process (Join-Path $app 'orosdrive.exe') -ArgumentList 'run', '--no-tray', '--background' -WindowStyle Hidden
for ($i = 0; $i -lt 50 -and -not (Test-Path "$cfg\instance.json"); $i++) { Start-Sleep -Milliseconds 200 }
if (-not (Get-Process orosdrive -ErrorAction SilentlyContinue)) { Fail 'the app did not start' }
Install
if (Get-Process orosdrive -ErrorAction SilentlyContinue) { Fail 'the app still runs after the upgrade' }
Write-Host 'upgrade over the running app: ok'

# Uninstall (silent: the settings question is skipped, so they stay).
Start-Process $app\unins000.exe -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART' -Wait
for ($i = 0; $i -lt 100 -and (Test-Path "$app\orosdrive.exe"); $i++) { Start-Sleep -Milliseconds 300 }
if (Test-Path "$app\orosdrive.exe") { Fail 'files left after uninstall' }
if (Test-Path $lnk) { Fail 'shortcut left after uninstall' }
if ($null -ne (Get-ItemProperty $run).'orOS Drive') { Fail 'Run value left after uninstall' }
if (-not (Test-Path $cfg)) { Fail "a silent uninstall removed the settings" }
Write-Host 'setup test ok'
