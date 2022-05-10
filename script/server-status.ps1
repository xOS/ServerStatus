#Get server and key
param($server, $key)
# Download latest release from github
$repo = "xos/serverstatus"
#  x86 or x64
if ([System.Environment]::Is64BitOperatingSystem) {
    $file = "server-agent_windows_amd64.zip"
}
else {
    $file = "server-agent_windows_386.zip"
}
$releases = "https://api.github.com/repos/$repo/releases"
#重复运行自动更新
if (Test-Path "C:\server-status") {
    Write-Host "Server Status monitoring already exists, delete and reinstall" -BackgroundColor DarkGreen -ForegroundColor White
    C:/server-status/nssm.exe stop server-status
    C:/server-status/nssm.exe remove server-status
    Remove-Item "C:\server-status" -Recurse
}
#TLS/SSL
Write-Host "Determining latest server-status release" -BackgroundColor DarkGreen -ForegroundColor White
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$tag = (Invoke-WebRequest -Uri $releases -UseBasicParsing | ConvertFrom-Json)[0].tag_name
#Region判断
$ipapi= Invoke-RestMethod  -Uri "https://api.myip.com/" -UserAgent "Mozilla/5.0 (Windows NT 6.1; WOW64) AppleWebKit/535.1 (KHTML, like Gecko) Chrome/14.0.835.163 Safari/535.1"
$region=$ipapi.cc
echo $ipapi
if($region -ne "CN"){
$download = "https://github.com/$repo/releases/download/$tag/$file"
Write-Host "Overseas machine("$region") direct connection!" -BackgroundColor DarkRed -ForegroundColor Green
echo $download
}elseif($region -eq $null){
cls
$download = "https://ghproxy.com/github.com/$repo/releases/download/$tag/$file"
Write-Host "Error,Most of the time, it is caused by the domestic network environment,use ghproxy.com" -BackgroundColor DarkRed -ForegroundColor Green
echo $download
}else{
$download = "https://ghproxy.com/github.com/$repo/releases/download/$tag/$file"
Write-Host "China's servers will be downloaded using the image address" -BackgroundColor DarkRed -ForegroundColor Green
echo $download
}
Invoke-WebRequest $download -OutFile "C:\server-status.zip"
#使用nssm安装服务
Invoke-WebRequest "http://nssm.cc/release/nssm-2.24.zip" -OutFile "C:\nssm.zip"
#解压
Expand-Archive "C:\server-status.zip" -DestinationPath "C:\temp" -Force
Expand-Archive "C:\nssm.zip" -DestinationPath "C:\temp" -Force
if (!(Test-Path "C:\server-status")) { New-Item -Path "C:\server-status" -type directory }
#整理文件
Move-Item -Path "C:\temp\server-agent.exe" -Destination "C:\server-status\server-agent.exe"
if ($file = "server-agent_windows_amd64.zip") {
    Move-Item -Path "C:\temp\nssm-2.24\win64\nssm.exe" -Destination "C:\server-status\nssm.exe"
}
else {
    Move-Item -Path "C:\temp\nssm-2.24\win32\nssm.exe" -Destination "C:\server-status\nssm.exe"
}
#清理垃圾
Remove-Item "C:\server-status.zip"
Remove-Item "C:\nssm.zip"
Remove-Item "C:\temp" -Recurse
#安装部分
C:\server-status\nssm.exe install server-status C:\server-status\server-agent.exe -s $server -p $key -d 
C:\server-status\nssm.exe start server-status
#enjoy
Write-Host "Enjoy It!" -BackgroundColor DarkGreen -ForegroundColor Red
