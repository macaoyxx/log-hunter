# 发版打包脚本：构建 → release/<版本>/ → 压缩 zip → 删除临时文件（exe），目录内只留 zip
# 用法: ./release.ps1 -Version 0.0.1
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^\d+\.\d+\.\d+$')]
    [string]$Version
)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

# 1. 构建（失败即停，不产出残缺包）
wails build
if ($LASTEXITCODE -ne 0) { throw "wails build 失败（退出码 $LASTEXITCODE）" }

# 2. 版本目录（已存在则复用，允许重打包）
$dir = Join-Path "release" $Version
New-Item -ItemType Directory -Force -Path $dir | Out-Null

# 3. 拷贝 exe 进版本目录（打包后会被删除的临时文件）
$exe = Join-Path $dir "log-hunter.exe"
Copy-Item "build/bin/log-hunter.exe" $exe -Force

# 4. 压缩为最终产物
$zip = Join-Path $dir "log-hunter-v$Version-windows-amd64.zip"
Compress-Archive -Path $exe -DestinationPath $zip -Force

# 5. 删除临时文件，目录内只留 zip
Remove-Item $exe -Force

Write-Host "发版完成: $zip"
