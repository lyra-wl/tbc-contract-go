//go:build integration
// +build integration

package contract

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

const stablecoinDiagJSONPath = "lib/contract/testdata/stablecoin_test.json"

// TestStableCoin_MintDiagJSON 构建 CreateCoin 两笔交易（不广播），把 mint / coinNFT / funding 的 hex
// 与 GetNFT* txdata 及输入输出摘要写入 testdata/stablecoin_test.json，供 scripts/stablecoin_mint_compare.mjs
// 与 tbc-contract（TS 参考）逐段对照。
//
// 运行（需 testnet UTXO 与私钥）：
//
//	STABLECOIN_DIAG_JSON=1 RUN_REAL_COIN_TEST=1 TBC_NETWORK=testnet TBC_PRIVATE_KEY=... \
//	  go test -tags=integration -v ./lib/contract -run TestStableCoin_MintDiagJSON -count=1
//
// 然后（仓库根目录）：
//
//	node scripts/stablecoin_mint_compare.mjs
func TestStableCoin_MintDiagJSON(t *testing.T) {
	if os.Getenv("STABLECOIN_DIAG_JSON") != "1" {
		t.Skip("设置 STABLECOIN_DIAG_JSON=1 写入 lib/contract/testdata/stablecoin_test.json")
	}
	requireRealCoinRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	adminAddr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("地址: %v", err)
	}

	coinName := envOrDefault("COIN_NAME", "USD Test")
	coinSymbol := envOrDefault("COIN_SYMBOL", "USDT")
	coinDecimal := parseDecimalRange(t, "COIN_DECIMAL", 6)
	coinAmount := parsePositiveInt64(t, "COIN_AMOUNT", 100000000)

	sc, err := NewStableCoin(&FtParams{
		Name:    coinName,
		Symbol:  coinSymbol,
		Amount:  coinAmount,
		Decimal: coinDecimal,
	})
	if err != nil {
		t.Fatalf("NewStableCoin: %v", err)
	}

	fundMin := envFloatOrDefault("COIN_FUNDING_MIN_TBC", 0.02)
	utxo := requireFundingUTXO(t, "FetchUTXO(diag)", adminAddr.AddressString, network, fundMin)
	utxoTX, err := api.FetchTXRaw(hex.EncodeToString(utxo.TxID), network)
	if err != nil {
		t.Fatalf("FetchTXRaw(utxo): %v", err)
	}

	txraws, err := sc.CreateCoin(privKey, adminAddr.AddressString, utxo, utxoTX, "")
	if err != nil {
		t.Fatalf("CreateCoin: %v", err)
	}
	if len(txraws) != 2 {
		t.Fatalf("CreateCoin 返回交易数=%d", len(txraws))
	}

	coinNftTx, err := bt.NewTxFromString(txraws[0])
	if err != nil {
		t.Fatalf("parse coinNFT: %v", err)
	}
	mintTx, err := bt.NewTxFromString(txraws[1])
	if err != nil {
		t.Fatalf("parse mint: %v", err)
	}

	cur, err := util.GetNFTCurrentTxdata(mintTx)
	if err != nil {
		t.Fatalf("GetNFTCurrentTxdata(mint): %v", err)
	}
	pre, err := util.GetNFTPreTxdata(coinNftTx)
	if err != nil {
		t.Fatalf("GetNFTPreTxdata(coinNft): %v", err)
	}
	prepre, err := util.GetNFTPrePreTxdata(utxoTX)
	if err != nil {
		t.Fatalf("GetNFTPrePreTxdata(funding): %v", err)
	}

	out := map[string]interface{}{
		"_readme": []string{
			"go 段由 TestStableCoin_MintDiagJSON 生成；运行 node scripts/stablecoin_mint_compare.mjs 合并 tsReference 与 comparison。",
			"与 tbc-contract stableCoin.createCoin / lib/util/nftunlock 对齐核对。",
		},
		"meta": map[string]interface{}{
			"generatedBy":       "go/TestStableCoin_MintDiagJSON",
			"generatedAt":       time.Now().UTC().Format(time.RFC3339),
			"network":           network,
			"adminAddress":      adminAddr.AddressString,
			"coinName":          coinName,
			"coinSymbol":        coinSymbol,
			"coinDecimal":       coinDecimal,
			"coinAmount":        coinAmount,
			"contractTxidGo":    sc.ContractTxid,
			"tapeScriptHexGo":   sc.TapeScript,
			"codeScriptHexGo":   sc.CodeScript,
			"fundingUtxoSat":  utxo.Satoshis,
			"fundingUtxoTxid": hex.EncodeToString(utxo.TxID),
			"fundingUtxoVout": utxo.Vout,
		},
		"rawHex": map[string]interface{}{
			"fundingTx": utxoTX.String(),
			"coinNftTx": txraws[0],
			"mintTx":    txraws[1],
		},
		"go": map[string]interface{}{
			"nftTxdata": map[string]string{
				"getNFTCurrentTxdata_mint":     cur,
				"getNFTPreTxdata_coinNft":    pre,
				"getNFTPrePreTxdata_funding": prepre,
				"concatCurPrePrePreHex":      cur + prepre + pre,
			},
			"mintTx":   txSummary(mintTx),
			"coinNft":  txSummary(coinNftTx),
			"funding":  txSummary(utxoTX),
			"mintInput0UnlockingHex": hex.EncodeToString(mintTx.Inputs[0].UnlockingScript.Bytes()),
		},
	}

	path := stablecoinDiagJSONPath
	if p := strings.TrimSpace(os.Getenv("STABLECOIN_DIAG_OUT")); p != "" {
		path = p
	}

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("已写入诊断 JSON: %s（请运行 node scripts/stablecoin_mint_compare.mjs）", path)
}

func txSummary(tx *bt.Tx) map[string]interface{} {
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
		outs = append(outs, map[string]interface{}{
			"index":    i,
			"satoshis": o.Satoshis,
			"scriptHexLen": func() int {
				if o.LockingScript == nil {
					return 0
				}
				return len(o.LockingScript.Bytes())
			}(),
			"scriptHexPrefix": scriptPrefixHex(o.LockingScript, 80),
		})
	}
	return map[string]interface{}{
		"version":      tx.Version,
		"lockTime":     tx.LockTime,
		"inputCount":   len(tx.Inputs),
		"outputCount":  len(tx.Outputs),
		"inputs":       in,
		"outputs":      outs,
		"txid": tx.TxID(),
	}
}

func scriptPrefixHex(s *bscript.Script, n int) string {
	if s == nil {
		return ""
	}
	b := s.Bytes()
	if len(b) <= n {
		return hex.EncodeToString(b)
	}
	return hex.EncodeToString(b[:n]) + "..."
}
