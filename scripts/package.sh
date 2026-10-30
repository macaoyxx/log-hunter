#!/usr/bin/env bash
# 本地打包脚本：构建 → release/<版本>/ → 压缩 zip → 删除临时 exe（目录内只留 zip）
# 仅打包不发布；发布走 publish.sh（先打包后经确认门发布）
# 用法: ./scripts/package.sh 0.0.3
set -euo pipefail
cd "$(dirname "$0")/.."

Version="${1:?用法: ./scripts/package.sh <x.y.z>}"
if [[ ! "$Version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "版本号格式应为 x.y.z，收到: $Version" >&2
  exit 1
fi

# 平台推导：产物名与压缩后缀（Windows / Linux）
case "$(uname -s)" in
  Linux*) os_name="linux";       bin_name="log-hunter"     ;;
  *)      os_name="windows";     bin_name="log-hunter.exe" ;;
esac
arch="$(uname -m)"
[[ "$arch" = "x86_64" ]] && arch="amd64"
plat="$os_name-$arch"

# 1. 构建（失败即停，不产出残缺包）；ldflags 注入版本号（main.version）
wails build -ldflags "-X main.version=$Version"

# 2. 版本目录（已存在则复用，允许重打包）
dir="release/$Version"
mkdir -p "$dir"

# 3. 拷贝产物进版本目录（压缩后会被删除的临时文件）
exe="$dir/$bin_name"
cp "build/bin/$bin_name" "$exe"

# 4. 压缩为最终产物：go run 标准库压缩（全平台一致——zip 命令非标配，
#    powershell 仅 Windows，Go 构建机必有）+ 落盘校验兜底
zip_path="$dir/log-hunter-v$Version-$plat.zip"
go run ./tools/packzip "$zip_path" "$exe"
if [[ ! -f "$zip_path" ]]; then
  echo "压缩失败：$zip_path 未生成" >&2
  exit 1
fi

# 5. 删除临时文件，目录内只留 zip
rm -f "$exe"

echo "打包完成: $zip_path"
