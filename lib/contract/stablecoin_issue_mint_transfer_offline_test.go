package contract

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

func issueMintTransferEnvOrDefault(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

// TestStableCoin_IssueMintTransferOfflineJSON 离线：CreateCoin（发行+首铸）→ MintCoin（增发），不广播。
// 不写 TransferCoin：首铸 mint 的 code 长度不满足 go-bt GetPreTxdata 对通用 FT 的 partialOffset 假设，离线 transfer 需链上/单独用例。
// 结果写入 testdata/stablecoin_test.json 的 issueMintTransferOffline，供 node scripts/stablecoin_issue_mint_transfer_offline.mjs 与 TS 对照。
//
//	go test ./lib/contract -run TestStableCoin_IssueMintTransferOfflineJSON -count=1 -v
//	cd ../.. && node scripts/stablecoin_issue_mint_transfer_offline.mjs
func TestStableCoin_IssueMintTransferOfflineJSON(t *testing.T) {
	const privWIF = "L1dzpqTvtKKYn2dYkMoBJSPYeBcYUpWNaXMWyME7gmJVCrifLW8x"

	dec, err := wif.DecodeWIF(privWIF)
	if err != nil {
		t.Fatalf("WIF: %v", err)
	}
	priv := dec.PrivKey
	adminAddr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		t.Fatalf("admin addr: %v", err)
	}
	recv := adminAddr.AddressString
	mintExtra := issueMintTransferEnvOrDefault("COIN_MINT_EXTRA", "50000")

	mkFund := func(dummyPrev string, fundOutSat uint64) (*bt.Tx, *bt.UTXO) {
		fundTx := newFTTx()
		dummyLock := "76a914" + strings.Repeat("22", 20) + "88ac"
		if err := fundTx.From(dummyPrev, 0, dummyLock, 20_000_000_000); err != nil {
			t.Fatalf("fundTx.From: %v", err)
		}
		outLs, err := bscript.NewP2PKHFromAddress(recv)
		if err != nil {
			t.Fatalf("p2pkh: %v", err)
		}
		fundTx.AddOutput(&bt.Output{LockingScript: outLs, Satoshis: fundOutSat})
		utxoTid, err := hex.DecodeString(fundTx.TxID())
		if err != nil {
			t.Fatalf("fund txid: %v", err)
		}
		utxo := &bt.UTXO{
			TxID:          utxoTid,
			Vout:          0,
			Satoshis:      fundOutSat,
			LockingScript: outLs,
		}
		return fundTx, utxo
	}

	fundCreate, utxoCreate := mkFund(strings.Repeat("33", 32), 15_000_000_000)
	fundMintFee, utxoMintFee := mkFund(strings.Repeat("44", 32), 15_000_000_000)
	fundXferFee, _ := mkFund(strings.Repeat("55", 32), 15_000_000_000)

	sc, err := NewStableCoin(&FtParams{
		Name:    "USD Test",
		Symbol:  "USDT",
		Amount:  100000000,
		Decimal: 6,
	})
	if err != nil {
		t.Fatalf("NewStableCoin: %v", err)
	}

	txraws, err := sc.CreateCoin(priv, recv, utxoCreate, fundCreate, "")
	if err != nil {
		t.Fatalf("CreateCoin: %v", err)
	}
	if len(txraws) != 2 {
		t.Fatalf("CreateCoin raws: got %d", len(txraws))
	}

	coinNftHex := txraws[0]
	mint0Hex := txraws[1]
	coinNftTx, err := bt.NewTxFromString(coinNftHex)
	if err != nil {
		t.Fatalf("coinNFT: %v", err)
	}
	mint0Tx, err := bt.NewTxFromString(mint0Hex)
	if err != nil {
		t.Fatalf("mint0: %v", err)
	}

	mintRaw1, err := sc.MintCoin(priv, recv, mintExtra, utxoMintFee, mint0Tx, coinNftTx, "")
	if err != nil {
		t.Fatalf("MintCoin: %v", err)
	}
	addBi := ParseDecimalToBigInt(mintExtra, sc.Decimal)
	if addBi.IsInt64() {
		sc.TotalSupply += addBi.Int64()
	}

	mint1Tx, err := bt.NewTxFromString(mintRaw1)
	if err != nil {
		t.Fatalf("mint1 parse: %v", err)
	}

	issue := map[string]interface{}{
		"meta": map[string]interface{}{
			"generatedBy":     "go/TestStableCoin_IssueMintTransferOfflineJSON",
			"generatedAt":     time.Now().UTC().Format(time.RFC3339),
			"network":         "offline",
			"privWIF":         privWIF,
			"adminAddress": recv,
			"mintExtra":    mintExtra,
			"contractTxidGo":  sc.ContractTxid,
			"codeScriptHexGo": sc.CodeScript,
			"tapeScriptHexGo": sc.TapeScript,
		},
		"rawHex": map[string]interface{}{
			"fundingCreate": fundCreate.String(),
			"fundingMint":   fundMintFee.String(),
			"fundingXfer":   fundXferFee.String(),
			"coinNftTx":     coinNftHex,
			"mintTx0":     mint0Hex,
			"mintTx1":     mintRaw1,
		},
		"go": map[string]interface{}{
			"coinNft":       stablecoinTxSummaryMap(coinNftTx),
			"mintTx0":       stablecoinTxSummaryMap(mint0Tx),
			"mintTx1":       stablecoinTxSummaryMap(mint1Tx),
			"fundingCreate": stablecoinTxSummaryMap(fundCreate),
			"fundingMint":   stablecoinTxSummaryMap(fundMintFee),
			"fundingXfer":   stablecoinTxSummaryMap(fundXferFee),
			"transferSkippedNote": "本 JSON 仅比对 CreateCoin + MintCoin；未包含 transfer raw。",
		},
	}

	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("wd: %v", err)
	}
	outPath := filepath.Join(pkgDir, "testdata", "stablecoin_test.json")

	var root map[string]interface{}
	if b, err := os.ReadFile(outPath); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &root)
	}
	if root == nil {
		root = map[string]interface{}{}
	}
	root["issueMintTransferOffline"] = issue
	if arr, ok := root["_readme"].([]interface{}); ok {
		const hint = "离线发行+首铸+增发（不广播）：go test ./lib/contract -run TestStableCoin_IssueMintTransferOfflineJSON -count=1 后 npm run stablecoin-issue-mint-transfer-offline 对照（无 transfer）。"
		found := false
		for _, x := range arr {
			if s, ok := x.(string); ok && strings.Contains(s, "IssueMintTransferOfflineJSON") {
				found = true
				break
			}
		}
		if !found {
			root["_readme"] = append([]interface{}{hint}, arr...)
		}
	} else if _, ok := root["_readme"]; !ok {
		root["_readme"] = []interface{}{
			"离线发行+首铸+增发（不广播）：go test ./lib/contract -run TestStableCoin_IssueMintTransferOfflineJSON -count=1 后 npm run stablecoin-issue-mint-transfer-offline 对照（无 transfer）。",
			"mint NFT txdata：node scripts/stablecoin_mint_compare.mjs（需顶层 rawHex.fundingTx/coinNftTx/mintTx）。",
		}
	}

	raw, jerr := json.MarshalIndent(root, "", "  ")
	if jerr != nil {
		t.Fatalf("json: %v", jerr)
	}
	if err := os.WriteFile(outPath, raw, 0644); err != nil {
		t.Fatalf("write %s: %v", outPath, err)
	}
	t.Logf("wrote issueMintTransferOffline to %s", outPath)
}
