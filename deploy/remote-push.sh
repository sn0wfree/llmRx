#!/bin/bash
# remote-push.sh — 从本机打包 llmRx 镜像并推送到远程服务器。
#
# 用法：
#   ./deploy/remote-push.sh <user@host> [port]     # 默认 SSH 端口 22
#
# 前置：能 `ssh <user@host>` 免密登录（装好公钥，见 REMOTE-DEPLOY.md §1）。
# 产物推送到服务器 /tmp/llmrx-deploy/，后续步骤见 REMOTE-DEPLOY.md。
set -euo pipefail

TARGET="${1:?用法: $0 <user@host> [ssh_port]}"
SSHPORT="${2:-22}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT=/tmp/llmrx-deploy
IMAGE=llmrx:local

cd "$ROOT"

echo "==> 1/4 构建镜像（已有二进制则跳过编译）"
if [ -f build/llmRx ]; then
  SKIP_GO_BUILD=1 sh scripts/build-docker.sh "$IMAGE"
else
  sh scripts/build-docker.sh "$IMAGE"
fi

echo "==> 2/4 打包镜像 + 部署资产"
rm -rf "$OUT" && mkdir -p "$OUT"
docker save "$IMAGE" | gzip > "$OUT/llmrx-image.tar.gz"
cp docker-compose.yml "$OUT/"
cp -r deploy/nginx-hardened "$OUT/"
ls -lh "$OUT"

echo "==> 3/4 推送到 $TARGET:/tmp/llmrx-deploy/"
scp -P "$SSHPORT" -r "$OUT" "$TARGET:/tmp/"

echo "==> 4/4 完成。接下来在服务器上执行（见 deploy/REMOTE-DEPLOY.md）："
cat <<EOF
  ssh -p $SSHPORT $TARGET
  cd /tmp/llmrx-deploy
  # 按手册执行 §2(加载镜像) §3(nginx) §4(首启改密) §5(验证)
EOF
