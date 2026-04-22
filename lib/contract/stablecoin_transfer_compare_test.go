//go:build integration
// +build integration

// 稳定币 Transfer 构造对照：拉链上 FT UTXO 与 fee UTXO，仅构建 transfer raw（不广播），写入 testdata/stablecoin_transfer_compare.json；
// 仓库根执行 node scripts/stablecoin_transfer_compare.mjs 用 tbc-contract 复现并比对。
//
//	需已铸造/有余额的管理员账户。示例：
//	  RUN_REAL_COIN_TEST=1 STABLECOIN_TRANSFER_COMPARE_JSON=1 TBC_NETWORK=testnet TBC_PRIVATE_KEY=<WIF> \
//	    COIN_CONTRACT_TXID=<首铸mint或索引id> \
//	    go test -tags=integration -v ./lib/contract -run TestStableCoin_TransferConstructCompareJSON -count=1
//	  cd ../.. && node scripts/stablecoin_transfer_compare.mjs
package contract

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

// TestStableCoin_TransferConstructCompareJSON 构建一笔 TransferCoin（不广播），把复现所需字段与 go.transferRawHex 写入 JSON。
func TestStableCoin_TransferConstructCompareJSON(t *testing.T) {
	if strings.TrimSpace(os.Getenv("STABLECOIN_TRANSFER_COMPARE_JSON")) != "1" {
		t.Skip("设置 STABLECOIN_TRANSFER_COMPARE_JSON=1 写入 testdata/stablecoin_transfer_compare.json")
	}
	network := setupStablecoinIntegration(t)
	privKey := loadPrivKey(t)
	contractTxid := mustEnv(t, "COIN_CONTRACT_TXID")
	toAddress := mustEnvOrConst(t, "COIN_TRANSFER_TO", defaultTransferTo)
	transferAmount := envOrDefault("COIN_TRANSFER_AMOUNT", "1000")

	sc := loadStableCoinForIntegration(t, network, contractTxid)
	fromAddr := coinAdminAddress(t, privKey)

	indexerID := contractTxid
	if si, err := api.FetchStableCoinInfo(contractTxid, network); err == nil && strings.TrimSpace(si.NftTXID) != "" {
		indexerID = strings.TrimSpace(si.NftTXID)
	} else if sid, err := api.StableCoinIndexerIDFromMintContractTx(contractTxid, network); err == nil && sid != "" {
		indexerID = sid
	}

	totalSupplyStr := ""
	dec := sc.Decimal
	name, sym := sc.Name, sc.Symbol
	codeH, tapeH := sc.CodeScript, sc.TapeScript
	if si, err := api.FetchStableCoinInfo(contractTxid, network); err == nil {
		totalSupplyStr = si.TotalSupply
		dec = int(si.Decimal)
		name, sym = si.Name, si.Symbol
		codeH, tapeH = si.CodeScript, si.TapeScript
	} else if si2, err := api.FetchStableCoinInfo(indexerID, network); err == nil {
		totalSupplyStr = si2.TotalSupply
		dec = int(si2.Decimal)
		name, sym = si2.Name, si2.Symbol
		codeH, tapeH = si2.CodeScript, si2.TapeScript
	}
	if totalSupplyStr == "" {
		t.Fatalf("无法解析 totalSupply，请确认 COIN_CONTRACT_TXID / 索引可访问")
	}

	amountBN := util.ParseDecimalToBigInt(transferAmount, sc.Decimal)
	ftCodeScript := BuildFTtransferCode(sc.CodeScript, fromAddr.AddressString)
	codeHex := hex.EncodeToString(ftCodeScript.Bytes())
	ftutxos := waitFtUtxosIndexed(t, contractTxid, fromAddr.AddressString, codeHex, network, amountBN, 35, 2*time.Second)

	preTXs, prepreTxDatas := fetchFtPreParentsForSpend(t, network, ftutxos)
	feeMin := envFloatOrDefault("COIN_TRANSFER_FEE_MIN_TBC", 0.01)
	feeUTXO := requireFundingUTXO(t, "FetchUTXO(fee)", fromAddr.AddressString, network, feeMin)

	txraw, err := sc.TransferCoin(privKey, toAddress, transferAmount, ftutxos, feeUTXO, preTXs, prepreTxDatas, 0)
	if err != nil {
		t.Fatalf("TransferCoin: %v", err)
	}

	ftOut := make([]map[string]interface{}, 0, len(ftutxos))
	for _, u := range ftutxos {
		ftOut = append(ftOut, map[string]interface{}{
			"txId":       u.TxID,
			"outputIndex": u.Vout,
			"script":     u.Script,
			"satoshis":   u.Satoshis,
			"ftBalance":  strings.TrimSpace(u.FtBalance),
		})
	}
	feeTid := hex.EncodeToString(feeUTXO.TxID)
	feeMap := map[string]interface{}{
		"txId":          feeTid,
		"outputIndex":   feeUTXO.Vout,
		"script":        feeUTXO.LockingScript.String(),
		"satoshis":      feeUTXO.Satoshis,
	}
	preHex := make([]string, 0, len(preTXs))
	for _, tx := range preTXs {
		preHex = append(preHex, tx.String())
	}

	doc := map[string]interface{}{
		"_readme": []string{
			"由 TestStableCoin_TransferConstructCompareJSON 生成；不广播。",
			"对照：仓库根 node scripts/stablecoin_transfer_compare.mjs（需 TBC_PRIVATE_KEY 与链上环境一致）。",
		},
		"meta": map[string]interface{}{
			"generatedBy":              "go/TestStableCoin_TransferConstructCompareJSON",
			"generatedAt":              time.Now().UTC().Format(time.RFC3339),
			"network":                  network,
			"coinContractTxidEnv":      contractTxid,
			"stablecoinIndexerId":      indexerID,
			"fromAddress":              fromAddr.AddressString,
			"toAddress":                toAddress,
			"transferAmount":           transferAmount,
			"feeUtxoTxid":              feeTid,
			"feeUtxoVout":              feeUTXO.Vout,
			"ftFeeSatPerKb":            feeSatPerKBFromEnv(),
			"feeMinTbc":                envFloatOrDefault("COIN_TRANSFER_FEE_MIN_TBC", 0.01),
			"lockstepFeeHint":          "JS 脚本从 inputs.feeUtxo 构造费 UTXO，与 Go 侧一致；勿另 fetchUTXO。",
		},
		"coinInfo": map[string]interface{}{
			"name":         name,
			"symbol":       sym,
			"decimal":      dec,
			"totalSupply":  totalSupplyStr,
			"codeScript":   codeH,
			"tapeScript":   tapeH,
			"contractTxid": contractTxid,
		},
		"inputs": map[string]interface{}{
			"ftUtxos":         ftOut,
			"feeUtxo":         feeMap,
			"preTxHexes":      preHex,
			"prepreTxDatas":   prepreTxDatas,
		},
		"go": map[string]interface{}{
			"transferRawHex": txraw,
		},
	}

	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("wd: %v", err)
	}
	outPath := filepath.Join(pkgDir, "testdata", "stablecoin_transfer_compare.json")
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	if err := os.WriteFile(outPath, raw, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("wrote %s", outPath)
}
