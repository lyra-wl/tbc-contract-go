#!/usr/bin/env bash
# 分别执行 Go（test.ts 同序组装）与 tbc-contract JS（--js-only，与 ft_1000_poolnft / test.ts 转账段同路径），
# 比对两行 raw hex 是否逐字节一致。
#
# 用法（在 tbc-contract-go 仓库根目录）：
#   export TBC_PRIVATE_KEY='...'   # 或从 tbc-contract/scripts/ft-compare.env 复制同名变量
#   export FT_CONTRACT_TXID=...
#   export FT_TRANSFER_TO=...
#   export FT_TRANSFER_AMOUNT=1000   # 可选
#   export TBC_NETWORK=testnet       # 可选
#
# 若两次运行各自 fetchUTXO(0.01) 可能选到不同手续费输入，raw 会不一致。请先注入同一费 UTXO：
#   export FT_LOCKSTEP_FEE_TXID='<txid>'
#   export FT_LOCKSTEP_FEE_VOUT=0
# （可与 tbc-contract 全量比对脚本先跑一遍，从其日志或调试输出里取费 UTXO。）
#
# 可选：TBC_CONTRACT_ROOT 指向 tbc-contract 根目录（默认：与本仓库同级的 tbc-contract）

set -euo pipefail

GO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
JS_ROOT="${TBC_CONTRACT_ROOT:-$(cd "$GO_ROOT/../tbc-contract" 2>/dev/null && pwd || true)}"

if [[ ! -d "$JS_ROOT" ]] || [[ ! -f "$JS_ROOT/scripts/ft-transfer-js-go-compare-broadcast.js" ]]; then
  echo "找不到 tbc-contract（需 scripts/ft-transfer-js-go-compare-broadcast.js）。请设置 TBC_CONTRACT_ROOT 或把 tbc-contract 放在与 tbc-contract-go 同级目录。" >&2
  exit 1
fi

if [[ -z "${TBC_PRIVATE_KEY:-}" ]] && [[ -z "${TBC_PRIVKEY:-}" ]]; then
  echo "请设置 TBC_PRIVATE_KEY 或 TBC_PRIVKEY（与 test.ts / ft-compare.env 一致）" >&2
  exit 1
fi
if [[ -z "${FT_CONTRACT_TXID:-${TBC_FT_CONTRACT_TXID:-}}" ]]; then
  echo "请设置 FT_CONTRACT_TXID（或 TBC_FT_CONTRACT_TXID）" >&2
  exit 1
fi
if [[ -z "${FT_TRANSFER_TO:-${TBC_FT_TRANSFER_TO:-}}" ]]; then
  echo "请设置 FT_TRANSFER_TO（或 TBC_FT_TRANSFER_TO）" >&2
  exit 1
fi

export FT_RAW_HEX_ONLY=1

GO_RAW="$(cd "$GO_ROOT" && go run ./cmd/ft_transfer_ts_mirror)"
JS_RAW="$(cd "$JS_ROOT" && node scripts/ft-transfer-js-go-compare-broadcast.js --js-only)"

GO_RAW="${GO_RAW//$'\r'/}"
GO_RAW="${GO_RAW//$'\n'/}"
JS_RAW="${JS_RAW//$'\r'/}"
JS_RAW="${JS_RAW//$'\n'/}"

if [[ "$GO_RAW" == "$JS_RAW" ]]; then
  echo "OK: Go 与 JS raw hex 一致（${#GO_RAW} 字符）"
  exit 0
fi

echo "FAIL: raw 不一致" >&2
echo "  Go len=${#GO_RAW}" >&2
echo "  JS len=${#JS_RAW}" >&2
if command -v cmp >/dev/null 2>&1; then
  printf '%s\n' "$GO_RAW" > /tmp/ft-ts-mirror-go.hex
  printf '%s\n' "$JS_RAW" > /tmp/ft-ts-mirror-js.hex
  cmp -l /tmp/ft-ts-mirror-go.hex /tmp/ft-ts-mirror-js.hex >&2 || true
fi
exit 1
