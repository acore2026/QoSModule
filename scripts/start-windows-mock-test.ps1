param(
    [string]$MockBind = "127.0.0.1:18081",
    [string]$TargetBind = "0.0.0.0:7400",
    [string]$RanPath = "/api/v1/qos/update",
    [string]$MockStatus = "ACCEPTED",
    [string]$MockMessage = "mock ran accepted",
    [switch]$SkipPortCheck
)

$ErrorActionPreference = "Stop"

function Get-PortNumber {
    param([string]$Endpoint)
    $parts = $Endpoint.Split(":")
    if ($parts.Length -lt 2) {
        throw "Invalid endpoint '$Endpoint'. Expected host:port."
    }
    return [int]$parts[$parts.Length - 1]
}

function Quote-PowerShellString {
    param([string]$Value)
    return "'" + $Value.Replace("'", "''") + "'"
}

function Test-TcpPortInUse {
    param([int]$Port)
    $connection = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
    return $null -ne $connection
}

function Test-UdpPortInUse {
    param([int]$Port)
    $endpoint = Get-NetUDPEndpoint -LocalPort $Port -ErrorAction SilentlyContinue
    return $null -ne $endpoint
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Split-Path -Parent $scriptDir
$targetDir = Join-Path $repoRoot "target\target"

if (-not (Test-Path (Join-Path $targetDir "go.mod"))) {
    throw "Cannot find target module at '$targetDir'. Run this script from the QoSModule checkout."
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go is not installed or not available in PATH."
}

$mockPort = Get-PortNumber $MockBind
$targetPort = Get-PortNumber $TargetBind

if (-not $SkipPortCheck) {
    if (Test-TcpPortInUse $mockPort) {
        throw "TCP port $mockPort is already in use. Choose another -MockBind, for example 127.0.0.1:18082."
    }
    if (Test-UdpPortInUse $targetPort) {
        throw "UDP port $targetPort is already in use. Choose another -TargetBind, for example 0.0.0.0:7401."
    }
}

$psExe = "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe"
if (-not (Test-Path $psExe)) {
    $psExe = "powershell.exe"
}

$targetDirQ = Quote-PowerShellString $targetDir
$mockBindQ = Quote-PowerShellString $MockBind
$targetBindQ = Quote-PowerShellString $TargetBind
$ranPathQ = Quote-PowerShellString $RanPath
$mockStatusQ = Quote-PowerShellString $MockStatus
$mockMessageQ = Quote-PowerShellString $MockMessage
$ranURLQ = Quote-PowerShellString ("http://$MockBind$RanPath")

$mockCommand = @"
`$Host.UI.RawUI.WindowTitle = 'QoS Mock RAN'
Set-Location -LiteralPath $targetDirQ
Write-Host 'Starting Mock RAN on $MockBind$RanPath'
go run .\cmd\mockran -b $mockBindQ -path $ranPathQ -status $mockStatusQ -message $mockMessageQ
"@

$targetCommand = @"
`$Host.UI.RawUI.WindowTitle = 'QoS Target'
Set-Location -LiteralPath $targetDirQ
Write-Host 'Starting QoS target on UDP $TargetBind'
Write-Host 'RAN endpoint: http://$MockBind$RanPath'
go run .\cmd\target -mode qos -b $targetBindQ -ran-url $ranURLQ
"@

Start-Process -FilePath $psExe -ArgumentList @("-NoExit", "-ExecutionPolicy", "Bypass", "-Command", $mockCommand)
Start-Sleep -Seconds 1
Start-Process -FilePath $psExe -ArgumentList @("-NoExit", "-ExecutionPolicy", "Bypass", "-Command", $targetCommand)

Write-Host ""
Write-Host "Started local QoS mock test services."
Write-Host "Mock RAN: http://$MockBind$RanPath"
Write-Host "Target UDP: $TargetBind"
Write-Host ""
Write-Host "Configure MASQUE Proxy target to one of:"
Write-Host "  Same Windows laptop: 127.0.0.1:$targetPort"
Write-Host "  Other machine:       <Windows-LAN-IP>:$targetPort"
Write-Host ""
Write-Host "If MASQUE Proxy runs on another machine, allow Windows Firewall inbound UDP $targetPort."
Write-Host "Close the two opened PowerShell windows to stop the services."
