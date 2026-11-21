#!/usr/bin/env bash
# 构建 mincode CLI（插件开发工具入口为 mincode plugin …）。
# 先检查模板里的 Go SDK、界面副本与插件运行时和源头一致，再交给
# tools/mincode-cli/build/main.go 在临时目录中嵌入全部模板并按目标平台编译
# （不读写 tools/mincode-cli/assets/）。
# 用法: build-plugin-dev-cli.sh <GOOS> <GOARCH|universal> <输出路径>
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GOOS_TARGET="${1:?usage: build-plugin-dev-cli.sh <goos> <goarch|universal> <output>}"
GOARCH_TARGET="${2:?usage: build-plugin-dev-cli.sh <goos> <goarch|universal> <output>}"
OUTPUT="${3:?usage: build-plugin-dev-cli.sh <goos> <goarch|universal> <output>}"

# 调用方可能传相对路径（相对其工作目录），在切换目录前先绝对化。
case "${OUTPUT}" in
  /*|[A-Za-z]:/*) ;;
  *) OUTPUT="${PWD}/${OUTPUT}" ;;
esac

CLI_DIR="${ROOT_DIR}/tools/mincode-cli"

cd "${ROOT_DIR}"

# 副本检查默认失败即停止（发布打包）。开发路径
# 设置 MINCODE_CLI_COPY_CHECK=warn：未同步的源头改动只打印警告，开发窗口照常打开。
copy_check() {
  if "$@"; then return 0; fi
  if [[ "${MINCODE_CLI_COPY_CHECK:-}" == "warn" ]]; then
    echo "警告：模板副本与源头不一致（$*），开发构建继续；发布打包会因此失败，请先同步这些副本。" >&2
    return 0
  fi
  return 1
}

# Go 模板依赖已发布的 github.com/mincodeai/plugin-sdk-go（仓库内经 replace 指向源码），没有副本要检查。
copy_check node tools/plugin-sdk/sync-plugin-ui.mjs --all --check
copy_check node vue-app/scripts/build-plugin-runtime.mjs --check
copy_check node vue-app/scripts/build-plugin-contracts.mjs --check

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  export MINCODE_CLI_SOURCE=export
fi

# 目标平台经 MINCODE_GOOS/MINCODE_GOARCH 只传给构建器内部的 go build。调用方导出的
# GOOS/GOARCH（如交叉打包时）必须去掉，否则 go run 会把构建器本身交叉编译，无法在本机运行。
build_cli() {
  (cd "${CLI_DIR}" && env -u GOOS -u GOARCH MINCODE_GOOS="${GOOS_TARGET}" MINCODE_GOARCH="$1" go run ./build --skip-tests "$2")
}

if [[ "${GOARCH_TARGET}" == "universal" ]]; then
  scratch="$(mktemp -d)"
  trap 'rm -rf "${scratch}"' EXIT
  build_cli amd64 "${scratch}/cli-amd64"
  build_cli arm64 "${scratch}/cli-arm64"
  mkdir -p "$(dirname "${OUTPUT}")"
  lipo -create -output "${OUTPUT}" "${scratch}/cli-amd64" "${scratch}/cli-arm64"
else
  build_cli "${GOARCH_TARGET}" "${OUTPUT}"
fi
chmod +x "${OUTPUT}"
echo "DevKit CLI: ${OUTPUT}"
