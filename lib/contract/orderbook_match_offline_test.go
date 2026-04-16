//go:build integration
// +build integration

package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
)

type obMatchSummaryOut struct {
	I            int    `json:"i"`
	Satoshis     uint64 `json:"satoshis"`
	ScriptSha256 string `json:"scriptSha256"`
}

type obMatchSummaryIn struct {
	I            int    `json:"i"`
	PrevTxID     string `json:"prevTxId"`
	OutputIndex  uint32 `json:"outputIndex"`
	ScriptLen    int    `json:"scriptLen,omitempty"`
	ScriptSha256 string `json:"scriptSha256,omitempty"`
}

type obMatchSummary struct {
	InputCount   int                 `json:"inputCount"`
	OutputCount  int                 `json:"outputCount"`
	Inputs       []obMatchSummaryIn  `json:"inputs"`
	Outputs      []obMatchSummaryOut `json:"outputs"`
	RawHexLength int                 `json:"rawHexLength"`
}

type obTestJSON struct {
	Version       int             `json:"version"`
	Network       string          `json:"network"`
	UnitPrice     string          `json:"unitPrice"`
	SellVolume    string          `json:"sellVolume"`
	BuyVolume     string          `json:"buyVolume"`
	FeeRate       string          `json:"feeRate"`
	SellOrderTxid string          `json:"sellOrderTxid"`
	BuyOrderTxid  string          `json:"buyOrderTxid"`
	JS            json.RawMessage `json:"js"`
	Go            json.RawMessage `json:"go"`
	Compare       json.RawMessage `json:"compare"`
	Note          string          `json:"note,omitempty"`
}

func summarizeMatchTxGo(t *testing.T, rawHex string) *obMatchSummary {
	t.Helper()
	tx, err := bt.NewTxFromString(rawHex)
	if err != nil {
		t.Fatalf("parse match tx: %v", err)
	}
	sum := &obMatchSummary{
		InputCount:   len(tx.Inputs),
		OutputCount:  len(tx.Outputs),
		RawHexLength: len(rawHex),
	}
	for i, out := range tx.Outputs {
		sb := out.LockingScript.Bytes()
		h := sha256.Sum256(sb)
		sum.Outputs = append(sum.Outputs, obMatchSummaryOut{
			I:            i,
			Satoshis:     out.Satoshis,
			ScriptSha256: hex.EncodeToString(h[:]),
		})
	}
	for i, in := range tx.Inputs {
		prev := in.PreviousTxID()
		prevHex := hex.EncodeToString(prev)
		us := in.UnlockingScript.Bytes()
		uh := sha256.Sum256(us)
		sum.Inputs = append(sum.Inputs, obMatchSummaryIn{
			I:            i,
			PrevTxID:     prevHex,
			OutputIndex:  in.PreviousTxOutIndex,
			ScriptLen:    len(us),
			ScriptSha256: hex.EncodeToString(uh[:]),
		})
	}
	return sum
}

func readOrderbookTestJSON(t *testing.T) ([]byte, *obTestJSON) {
	t.Helper()
	p := filepath.Join("testdata", "orderbook_test.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取 %s: %v（请先运行 node scripts/orderbook_match_offline_dump.mjs）", p, err)
	}
	var j obTestJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatalf("解析 JSON: %v", err)
	}
	return raw, &j
}

// TestOrderBook_Integration_MatchOfflineCompare 读取 testdata/orderbook_test.json 中 JS 已广播的卖/买 txid，
// 本地构建撮合 raw（不广播），与 js.summary 比对输出与输入 outpoint；结果写回同一 JSON。
func TestOrderBook_Integration_MatchOfflineCompare(t *testing.T) {
	requireRealOBRun(t)
	network := orderBookIntegrationNetwork(t)
	rawFile, j := readOrderbookTestJSON(t)
	if strings.TrimSpace(j.SellOrderTxid) == "" || strings.TrimSpace(j.BuyOrderTxid) == "" {
		t.Skip("orderbook_test.json 缺少 sellOrderTxid / buyOrderTxid")
	}

	matchPriv := obWIFEnvOrDefault(t, "OB_MATCH_WIF", obJSDefaultMatchWIF)

	ftContractTxid := envOrDefault("OB_FT_CONTRACT_TXID", obJSDefaultFTContract)
	ftFeeAddress := envOrDefault("OB_FT_FEE_ADDRESS", obJSDefaultFeeAddress)
	tbcFeeAddress := envOrDefault("OB_TBC_FEE_ADDRESS", obJSDefaultFeeAddress)

	addrMain := network == "mainnet"
	matchAddr, _ := bscript.NewAddressFromPublicKey(matchPriv.PubKey(), addrMain)

	ftInfo, err := api.FetchFtInfo(ftContractTxid, network)
	if err != nil {
		t.Fatalf("FetchFtInfo: %v", err)
	}
	_ = ftInfo

	sellTxid := j.SellOrderTxid
	buyTxid := j.BuyOrderTxid

	sellPreTX, err := api.FetchTXRaw(sellTxid, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(sell): %v", err)
	}
	buyPreTX, err := api.FetchTXRaw(buyTxid, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(buy): %v", err)
	}

	sellTxidBytes, _ := hex.DecodeString(sellTxid)
	buyTxidBytes, _ := hex.DecodeString(buyTxid)

	buyUTXO := &bt.UTXO{
		TxID:          buyTxidBytes,
		Vout:          0,
		Satoshis:      buyPreTX.Outputs[0].Satoshis,
		LockingScript: buyPreTX.Outputs[0].LockingScript,
	}
	ftVout := uint32(1)
	ftUTXO := &bt.UTXO{
		TxID:          buyTxidBytes,
		Vout:          ftVout,
		Satoshis:      buyPreTX.Outputs[ftVout].Satoshis,
		LockingScript: buyPreTX.Outputs[ftVout].LockingScript,
	}
	sellUTXO := &bt.UTXO{
		TxID:          sellTxidBytes,
		Vout:          0,
		Satoshis:      sellPreTX.Outputs[0].Satoshis,
		LockingScript: sellPreTX.Outputs[0].LockingScript,
	}

	ftPrePreTxData, err := api.FetchFtPrePreTxData(buyPreTX, int(ftVout), network)
	if err != nil {
		t.Fatalf("FetchFtPrePreTxData: %v", err)
	}

	tapeScript := buyPreTX.Outputs[ftVout+1].LockingScript.Bytes()
	ftTapeHex := hex.EncodeToString(tapeScript)
	ftCodeHex := ftUTXO.LockingScript.String()
	ftBalance := uint64(0)
	if len(tapeScript) >= 51 {
		bal := GetBalanceFromTape(hex.EncodeToString(tapeScript))
		if bal != nil {
			ftBalance = bal.Uint64()
		}
	}

	feeUTXO, err := api.FetchUTXO(matchAddr.AddressString, 0.02, network)
	if err != nil {
		t.Fatalf("FetchUTXO(match): %v", err)
	}

	ob := NewOrderBook()
	matchRaw, err := ob.MatchOrder(
		matchPriv,
		buyUTXO, buyPreTX,
		ftUTXO, buyPreTX, ftPrePreTxData,
		sellUTXO, sellPreTX,
		[]*bt.UTXO{feeUTXO},
		ftFeeAddress, tbcFeeAddress,
		ftCodeHex, ftTapeHex,
		ftBalance,
		ftContractTxid,
	)
	if err != nil {
		t.Fatalf("MatchOrder: %v", err)
	}

	goSum := summarizeMatchTxGo(t, matchRaw)

	var full map[string]json.RawMessage
	if err := json.Unmarshal(rawFile, &full); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	var jsBlock struct {
		Summary *obMatchSummary `json:"summary"`
	}
	if err := json.Unmarshal(full["js"], &jsBlock); err != nil || jsBlock.Summary == nil {
		t.Fatalf("JSON 缺少 js.summary: %v", err)
	}
	jsSum := jsBlock.Summary

	outputsMatch := compareOutputs(t, jsSum, goSum)
	inpointsMatch := compareInputOutpoints(t, jsSum, goSum)

	notes := []string{}
	if !outputsMatch {
		notes = append(notes, "outputs 的 satoshis 或 scriptSha256 不一致，检查 orderbook.go MatchOrder 与 orderBook.js matchOrder 找零/输出顺序")
	}
	if !inpointsMatch {
		notes = append(notes, "输入 prevTxId/vout 与 JS 不一致（若仅字节序相反可忽略）")
	}
	cmp := map[string]interface{}{
		"outputsMatch":        outputsMatch,
		"inputOutpointsMatch": inpointsMatch,
		"notes":               notes,
	}

	goBlock := map[string]interface{}{
		"generatedAt": time.Now().UTC().Format(time.RFC3339Nano),
		"matchRawHex": matchRaw,
		"summary":     goSum,
	}

	// 写回 JSON
	var root map[string]interface{}
	if err := json.Unmarshal(rawFile, &root); err != nil {
		t.Fatalf("unmarshal root: %v", err)
	}
	root["go"] = goBlock
	root["compare"] = cmp

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "orderbook_test.json"), out, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Logf("compare outputsMatch=%v inputOutpointsMatch=%v", outputsMatch, inpointsMatch)
	if !outputsMatch || !inpointsMatch {
		t.Errorf("与 JS 摘要不一致，已写入 testdata/orderbook_test.json 的 compare 字段")
	}
}

func compareOutputs(t *testing.T, js, gob *obMatchSummary) bool {
	t.Helper()
	if js == nil || gob == nil {
		return false
	}
	if js.OutputCount != gob.OutputCount {
		return false
	}
	for i := range js.Outputs {
		if i >= len(gob.Outputs) {
			return false
		}
		if js.Outputs[i].Satoshis != gob.Outputs[i].Satoshis {
			t.Logf("output[%d] sats js=%d go=%d", i, js.Outputs[i].Satoshis, gob.Outputs[i].Satoshis)
			return false
		}
		if js.Outputs[i].ScriptSha256 != gob.Outputs[i].ScriptSha256 {
			t.Logf("output[%d] scriptSha256 differ", i)
			return false
		}
	}
	return true
}

func compareInputOutpoints(t *testing.T, js, gob *obMatchSummary) bool {
	t.Helper()
	if js == nil || gob == nil || js.InputCount != gob.InputCount {
		return false
	}
	for i := range js.Inputs {
		a := strings.ToLower(strings.TrimSpace(js.Inputs[i].PrevTxID))
		b := strings.ToLower(strings.TrimSpace(gob.Inputs[i].PrevTxID))
		if a == reverseHexPairs(b) || a == b {
			if js.Inputs[i].OutputIndex != gob.Inputs[i].OutputIndex {
				return false
			}
			continue
		}
		t.Logf("input[%d] prev mismatch js=%s go=%s", i, a, b)
		return false
	}
	return true
}

func reverseHexPairs(h string) string {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) == 0 {
		return h
	}
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return hex.EncodeToString(b)
}
