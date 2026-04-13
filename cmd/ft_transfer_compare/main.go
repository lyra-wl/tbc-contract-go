// ft_transfer_compare：与 tbc-contract/scripts/ft-transfer-js-go-compare-broadcast.js 中 **Go 侧**逻辑一致
//（同一套 FetchFtUTXOs / FetchTXRaw / FetchFtPrePreTxData / FetchUTXO 或 LOCKSTEP 费 UTXO / contract.Transfer），
// 向 stdout 打印一行 JSON（raw_hex、txid、各输入 unlocking_script_hex），供 Node 与 JS 逐字节对照；本命令不广播。
//
// 组装实现见 internal/fttransfertestts（与 scripts/test.ts Transfer 块同序）。
//
// 环境变量与 JS 对比脚本对齐（别名同 docs/runners/chain/FT_COMPARE.md）：
//
//	必填：TBC_PRIVATE_KEY 或 TBC_PRIVKEY（WIF）
//	必填：FT_CONTRACT_TXID 或 TBC_FT_CONTRACT_TXID
//	必填：FT_TRANSFER_TO 或 TBC_FT_TRANSFER_TO
//	可选：TBC_NETWORK（默认 testnet）
//	可选：FT_TRANSFER_AMOUNT 或 TBC_FT_TRANSFER_AMOUNT（默认 1000）
//
// 用法（在 tbc-contract-go 目录）：
//
//	export TBC_PRIVATE_KEY='<WIF>'
//	export TBC_NETWORK=testnet
//	export FT_CONTRACT_TXID='<mint txid>'
//	export FT_TRANSFER_TO='<接收地址>'
//	export FT_TRANSFER_AMOUNT=1000
//	go run ./cmd/ft_transfer_compare
//
// 仅输出组装后的 raw hex（小写、无换行以外字符，便于 pipe / 复制到广播接口）：
//
//	FT_RAW_HEX_ONLY=1 go run ./cmd/ft_transfer_compare
//	# 或
//	FT_TRANSFER_COMPARE_OUTPUT=raw go run ./cmd/ft_transfer_compare
//
// 成功时 stdout 仅一行 hex；失败时仍为一行 JSON（ok=0, error=...）。可选将 txid 打到 stderr：FT_TRANSFER_COMPARE_LOG_TXID=1
//
// 可选：FT_LOCKSTEP_FEE_TXID、FT_LOCKSTEP_FEE_VOUT — 指定手续费 prevout（与 scripts/ft-transfer-js-go-compare-broadcast.js 联用时由脚本注入，避免与 JS 各选各的 UTXO）
//
// 可选：FT_TRANSFER_COMPARE_STEPS=1 — 在 JSON 中附带四步诊断（字段 / 无签名线 SHA256d / 各输入 sighash digest / txid），供与 JS 逐步对照。
//
// 仅用 Go raw、经 tbc-contract HTTP 广播：在 tbc-contract 根目录执行
// scripts/broadcast-go-ft-transfer-raw.js（见该文件头注释，需 BROADCAST_GO_FT_TRANSFER=1）。
//
// 费率：本工具用于与 tbc-contract JS「FT.transfer + feePerKb(80)」逐字节对齐。若继承 shell 的 FT_FEE_SAT_PER_KB（例如 500），找零与签名会与 JS 不一致。
// 默认在进程内强制 FT_FEE_SAT_PER_KB=80；仅调试 Go 侧非 80 费率时设 FT_COMPARE_RESPECT_FEE_ENV=1，此时沿用环境变量（未设置则仍为 contract.newFeeQuote80 默认 80）。
//
// 节点报 insufficient priority：可提高 FT_FEE_SAT_PER_KB；或与 JS 对齐后仍不足时设 FT_RELAY_FEE_SIGNED_ESTIMATE=1 且 FT_RELAY_SIGNED_UNLOCK_BYTES（> FT_SIGNED_UNLOCK_BYTES）进一步抬 fee。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sCrypt-Inc/tbc-contract-go/internal/fttransfertestts"
)

func main() {
	rep := fttransfertestts.AssembleFromEnvironment()
	if rep.OK != "1" {
		b, _ := json.Marshal(struct {
			OK    string `json:"ok"`
			Error string `json:"error"`
		}{OK: "0", Error: rep.Error})
		fmt.Println(string(b))
		os.Exit(1)
	}

	rawOut := strings.EqualFold(strings.TrimSpace(os.Getenv("FT_TRANSFER_COMPARE_OUTPUT")), "raw") ||
		strings.TrimSpace(os.Getenv("FT_RAW_HEX_ONLY")) == "1"
	if rawOut && strings.TrimSpace(os.Getenv("FT_TRANSFER_COMPARE_STEPS")) != "1" {
		if strings.TrimSpace(os.Getenv("FT_TRANSFER_COMPARE_LOG_TXID")) == "1" {
			fmt.Fprintf(os.Stderr, "txid=%s raw_hex_chars=%d\n", rep.TxID, len(rep.RawHex))
		}
		fmt.Println(rep.RawHex)
		return
	}

	b, err := json.Marshal(rep)
	if err != nil {
		em, _ := json.Marshal(struct {
			OK    string `json:"ok"`
			Error string `json:"error"`
		}{OK: "0", Error: fmt.Sprintf("JSON: %v", err)})
		fmt.Println(string(em))
		os.Exit(1)
	}
	fmt.Println(string(b))
}
