#!/usr/bin/env bash
# 发布脚本：先本地打包（调 package.sh）→ 发布确认门 → 推 tag → GitHub Release
# 用法: ./scripts/publish.sh 0.0.3
#
# ★ 发布确认门（写死的铁律，不可移除、不得新增任何跳过确认的参数或分支）：
#   发布动作（push tag / gh release create）前必须由人在终端输入 yes 确认。
#   任何自动化改造都不得绕过此门。
set -euo pipefail
cd "$(dirname "$0")/.."

Version="${1:?用法: ./scripts/publish.sh <x.y.z>}"
if [[ ! "$Version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "版本号格式应为 x.y.z，收到: $Version" >&2
  exit 1
fi

tag="v$Version"
notes="LogHunter $tag。变更详见仓库 docs/iterations/$Version/README.md。"

# 1. 先本地打包（构建 + zip，产物落在 release/<版本>/）
"$(dirname "$0")/package.sh" "$Version"

# 找刚生成的 zip（后缀含平台名，循环取最新一个）
zip_path="$(ls -t release/$Version/log-hunter-v$Version-*.zip | head -1)"
zip_size="$(du -h "$zip_path" | cut -f1)"

# 2. 发布确认门（铁律：仅接受完整 yes；其余任何输入一律取消，产物保留本地）
echo ""
echo "=== 发布预览 ==="
echo "Tag:        $tag → origin"
echo "Release:    $tag"
echo "产物:       $zip_path ($zip_size)"
echo "Notes:      $notes"
echo ""
read -r -p "确认发布到 GitHub？输入 yes 继续（其他任意输入取消）: " answer
if [[ "$answer" != "yes" ]]; then
  echo "已取消发布。产物保留在 $zip_path"
  echo "手动上传：https://github.com/macaoyxx/log-hunter/releases/new （选择 tag $tag，拖入 zip）"
  exit 0
fi

# 3. 打 tag 并推送（完整 refs 防 tag/分支同名歧义）。
#    重发防护：本地已有同 tag 时必须指向当前提交，否则产物与 tag 不一致——警告退出
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  tagged=$(git rev-parse "refs/tags/$tag"^{commit})
  head=$(git rev-parse HEAD)
  if [[ "$tagged" != "$head" ]]; then
    echo "本地已有 $tag 且指向别的提交（重发场景）：请先 git tag -d $tag 再运行" >&2
    exit 1
  fi
else
  git tag "$tag"
fi
git push origin "refs/tags/$tag"

# 4. gh 创建 Release 并上传产物；gh 不可用时降级为手动上传指引（不算失败）
if command -v gh >/dev/null 2>&1; then
  gh release create "$tag" "$zip_path" --title "$tag" --notes "$notes"
  echo "已发布: https://github.com/macaoyxx/log-hunter/releases/tag/$tag"
else
  echo "gh CLI 不可用，tag 已推送。手动上传：https://github.com/macaoyxx/log-hunter/releases/new （选择 tag $tag，拖入 $zip_path）"
  echo "安装 gh 后可全自动：winget install GitHub.cli && gh auth login"
fi
