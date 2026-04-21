package contract

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

// 与 stablecoin_mint_diag_test 中 txSummary 同形，供离线 JSON 与 JS 脚本消费。
func stablecoinTxSummaryMap(tx *bt.Tx) map[string]interface{} {
	in := make([]map[string]interface{}, 0, len(tx.Inputs))
	for i, x := range tx.Inputs {
		prev := hex.EncodeToString(x.PreviousTxID())
		ls := ""
		if x.PreviousTxScript != nil {
			ls = hex.EncodeToString(x.PreviousTxScript.Bytes())
		}
		un := ""
		if x.UnlockingScript != nil {
			un = hex.EncodeToString(x.UnlockingScript.Bytes())
		}
		in = append(in, map[string]interface{}{
			"index":               i,
			"prevTxIdHex":         prev,
			"prevTxOutIndex":      x.PreviousTxOutIndex,
			"sequenceNumber":      x.SequenceNumber,
			"previousTxScriptHex": ls,
			"unlockingScriptHex":  un,
		})
	}
	outs := make([]map[string]interface{}, 0, len(tx.Outputs))
	for i, o := range tx.Outputs {
		slen := 0
		if o.LockingScript != nil {
			slen = len(o.LockingScript.Bytes())
		}
		prefix := ""
		if o.LockingScript != nil {
			b := o.LockingScript.Bytes()
			n := 80
			if len(b) < n {
				n = len(b)
			}
			prefix = hex.EncodeToString(b[:n])
			if len(b) > n {
				prefix += "..."
			}
		}
		outs = append(outs, map[string]interface{}{
			"index":           i,
			"satoshis":        o.Satoshis,
			"scriptHexLen":    slen,
			"scriptHexPrefix": prefix,
		})
	}
	return map[string]interface{}{
		"version":     tx.Version,
		"lockTime":    tx.LockTime,
		"inputCount":  len(tx.Inputs),
		"outputCount": len(tx.Outputs),
		"inputs":      in,
		"outputs":     outs,
		"txid":        tx.TxID(),
	}
}

// TestStableCoin_MintNFTTxdataOffline 本地构造 funding → coinNFT → mint（不广播、不拉索引器），
// 写入 testdata/stablecoin_mint_offline.json；可选运行仓库根 scripts/stablecoin_mint_compare.mjs 做 Go/TS 十六进制对照。
//
//	go test ./lib/contract -run TestStableCoin_MintNFTTxdataOffline -count=1
//	cd ../.. && node scripts/stablecoin_mint_compare.mjs lib/contract/testdata/stablecoin_mint_offline.json
//
// 在 package 目录执行 node 时第二参数为相对 lib/contract 的路径；在仓库根则用 tbc-contract-go/lib/contract/testdata/...
func TestStableCoin_MintNFTTxdataOffline(t *testing.T) {
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

	// 一笔仅用于 prePre txdata 的「父交易」：输入不合法链上，但 TxID 与输出可供 CreateCoin 使用。
	fundTx := newFTTx()
	dummyPrev := strings.Repeat("33", 32)
	dummyLock := "76a914" + strings.Repeat("22", 20) + "88ac"
	if err := fundTx.From(dummyPrev, 0, dummyLock, 20_000_000_000); err != nil {
		t.Fatalf("fundTx.From: %v", err)
	}
	outLs, err := bscript.NewP2PKHFromAddress(adminAddr.AddressString)
	if err != nil {
		t.Fatalf("p2pkh: %v", err)
	}
	const fundOutSat = uint64(15_000_000_000)
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

	sc, err := NewStableCoin(&FtParams{
		Name:    "USD Test",
		Symbol:  "USDT",
		Amount:  100000000,
		Decimal: 6,
	})
	if err != nil {
		t.Fatalf("NewStableCoin: %v", err)
	}

	txraws, err := sc.CreateCoin(priv, recv, utxo, fundTx, "")
	if err != nil {
		t.Fatalf("CreateCoin: %v", err)
	}
	if len(txraws) != 2 {
		t.Fatalf("CreateCoin raws: got %d", len(txraws))
	}

	coinNftTx, err := bt.NewTxFromString(txraws[0])
	if err != nil {
		t.Fatalf("coinNFT: %v", err)
	}
	mintTx, err := bt.NewTxFromString(txraws[1])
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	cur, err := util.GetNFTCurrentTxdata(mintTx)
	if err != nil {
		t.Fatalf("GetNFTCurrentTxdata: %v", err)
	}
	pre, err := util.GetNFTPreTxdata(coinNftTx)
	if err != nil {
		t.Fatalf("GetNFTPreTxdata: %v", err)
	}
	prepre, err := util.GetNFTPrePreTxdata(fundTx)
	if err != nil {
		t.Fatalf("GetNFTPrePreTxdata: %v", err)
	}

	out := map[string]interface{}{
		"_readme": []string{
			"离线构造：不广播。由 TestStableCoin_MintNFTTxdataOffline 生成。",
			"对照：在仓库根 node scripts/stablecoin_mint_compare.mjs tbc-contract-go/lib/contract/testdata/stablecoin_mint_offline.json",
		},
		"meta": map[string]interface{}{
			"generatedBy":  "go/TestStableCoin_MintNFTTxdataOffline",
			"generatedAt":  time.Now().UTC().Format(time.RFC3339),
			"network":      "offline",
			"adminAddress": recv,
		},
		"rawHex": map[string]interface{}{
			"fundingTx": fundTx.String(),
			"coinNftTx": txraws[0],
			"mintTx":    txraws[1],
		},
		"go": map[string]interface{}{
			"nftTxdata": map[string]string{
				"getNFTCurrentTxdata_mint":     cur,
				"getNFTPreTxdata_coinNft":      pre,
				"getNFTPrePreTxdata_funding":   prepre,
				"concatCurPrePrePreHex":        cur + prepre + pre,
			},
			"mintTx":                 stablecoinTxSummaryMap(mintTx),
			"coinNft":                stablecoinTxSummaryMap(coinNftTx),
			"funding":                stablecoinTxSummaryMap(fundTx),
			"mintInput0UnlockingHex": hex.EncodeToString(mintTx.Inputs[0].UnlockingScript.Bytes()),
		},
	}

	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("wd: %v", err)
	}
	outPath := filepath.Join(pkgDir, "testdata", "stablecoin_mint_offline.json")
	raw, jerr := json.MarshalIndent(out, "", "  ")
	if jerr != nil {
		t.Fatalf("json: %v", jerr)
	}
	if err := os.WriteFile(outPath, raw, 0644); err != nil {
		t.Fatalf("write %s: %v", outPath, err)
	}
	t.Logf("wrote %s", outPath)

	// 可选：与 node_modules/tbc-contract 对照（仓库根需在「working project」下且已 npm install）
	if os.Getenv("SKIP_STABLECOIN_OFFLINE_NODE") == "1" {
		t.Skip("SKIP_STABLECOIN_OFFLINE_NODE=1，跳过 node 对照")
	}
	script := filepath.Join(pkgDir, "..", "..", "..", "scripts", "stablecoin_mint_compare.mjs")
	if _, stat := os.Stat(script); stat != nil {
		t.Logf("skip node compare: script not found at %s (%v)", script, stat)
		return
	}
	jsonArg, err := filepath.Abs(outPath)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	cmd := exec.Command("node", script, jsonArg)
	cmd.Dir = filepath.Join(pkgDir, "..", "..", "..")
	outB, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node compare: %v\n%s", err, string(outB))
	}
	t.Logf("node compare:\n%s", string(outB))

	// 读回 comparison，任一为 false 则失败
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cmp, ok := doc["comparison"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing comparison in JSON")
	}
	for _, k := range []string{
		"getNFTCurrentTxdata_mint_goEqualsTs",
		"getNFTPreTxdata_goEqualsTs",
		"getNFTPrePreTxdata_goEqualsTs",
		"concatUnlockOrder_goEqualsTs",
		"goMirrorCurrentTxdata_equals_tsCurrent",
	} {
		v, ok := cmp[k].(bool)
		if !ok || !v {
			t.Errorf("%s = %v (want true)", k, cmp[k])
		}
	}
}
