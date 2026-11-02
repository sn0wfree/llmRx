#!/bin/bash
export PATH="$HOME/.local/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
export GOSUMDB=off
export CGO_ENABLED=1
cd /mnt/d/Github/llmRx || exit 1
go build ./... 2>&1 | grep -vE 'sqlite3-binding|zTail|\^|^\s*[0-9]+ \|' | head -20
echo "BUILD_DONE"
