$env:CGO_ENABLED = '0'
$env:GOPROXY = 'https://goproxy.cn,direct'
$goExe = 'C:\Program Files\Go\bin\go.exe'

Write-Host "Syncing public assets..."
Copy-Item "$PSScriptRoot\public\*" -Destination "$PSScriptRoot\pkg\web\public\" -Recurse -Force

Write-Host "Compiling Linux AMD64..."
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
& $goExe build -ldflags='-s -w' -o "$PSScriptRoot\edgetunnel-lb-linux-amd64" "$PSScriptRoot\cmd\server"

Write-Host "Compiling Linux ARM64..."
$env:GOOS = 'linux'
$env:GOARCH = 'arm64'
& $goExe build -ldflags='-s -w' -o "$PSScriptRoot\edgetunnel-lb-linux-arm64" "$PSScriptRoot\cmd\server"

Write-Host "Done!"
Get-Item "$PSScriptRoot\edgetunnel-lb-linux-amd64", "$PSScriptRoot\edgetunnel-lb-linux-arm64" | Select-Object Name, Length, LastWriteTime