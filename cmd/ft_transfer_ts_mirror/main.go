// ft_transfer_ts_mirror：仅实现与 tbc-contract/scripts/test.ts 中 Transfer 代码块（约 71–114 行）
// 同序的 FT 转账组装，便于与 JS 侧同一流程输出的 raw 做逐字节比对。
//
// 与 cmd/ft_transfer_compare 共用 internal/fttransfertestts，输出格式与环境变量完全一致；
// 本命令文档侧重「test.ts 镜像」，用于测试与对照命名。
//
// 环境变量（与 test.ts 中 network / 私钥 / 合约 / 收款地址 / 金额 对应）：
//
//	TBC_PRIVATE_KEY 或 TBC_PRIVKEY（WIF）— 对应 test.ts privateKeyA
//	TBC_NETWORK（默认 testnet）
//	FT_CONTRACT_TXID — 对应 ftContractTxid
//	FT_TRANSFER_TO — 对应 addressB
//	FT_TRANSFER_AMOUNT（默认 1000）— 对应 transferTokenAmount
//
// 手续费：与 test.ts 一致，tbc_amount=0 时 FetchUTXO(from, 0.01)。若需与某次 JS 运行选同一笔费 UTXO，请设
// FT_LOCKSTEP_FEE_TXID、FT_LOCKSTEP_FEE_VOUT（与 ft-transfer-js-go-compare-broadcast.js 一致）。
//
// 输出：
//
//	默认：一行 JSON（raw_hex、txid、各输入 unlocking_script_hex）
//	FT_RAW_HEX_ONLY=1：仅一行小写 hex
//	FT_TRANSFER_COMPARE_STEPS=1：JSON 内含四步诊断字段 steps
//
// 与 tbc-contract JS 比对 raw（需在两边加载相同环境变量；费 UTXO 建议 LOCKSTEP）：
//
//	# 在 tbc-contract-go 目录
//	bash scripts/compare_ft_transfer_test_ts_raw.sh
//
// 或手动：
//
//	FT_RAW_HEX_ONLY=1 go run ./cmd/ft_transfer_ts_mirror > /tmp/go.raw
//	cd ../tbc-contract && node scripts/ft-transfer-js-go-compare-broadcast.js --js-only > /tmp/js.raw
//	cmp /tmp/go.raw /tmp/js.raw
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
			fmt.Fprintf(os.Stderr, "[ft_transfer_ts_mirror] txid=%s raw_hex_chars=%d\n", rep.TxID, len(rep.RawHex))
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
