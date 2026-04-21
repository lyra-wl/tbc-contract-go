// piggy_freeze_compare：与 cmd/piggy_freeze_run 相同参数来源，仅输出 JSON（stdout），不广播；供 scripts/piggy_freeze_compare.mjs 与 JS 侧对照。
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/contract"
)

const piggyDefaultFetchUTXOAddress = "1P2toD4aKcUsxhTCbUjz5mcd3ajAJB9G1W"

type outputRow struct {
	Index          int    `json:"index"`
	Satoshis       uint64 `json:"satoshis"`
	ScriptHexLen   int    `json:"scriptHexLen"`
	ScriptHex      string `json:"scriptHex,omitempty"`
	LikelyPiggyOut bool   `json:"likelyPiggyBankScript"`
}

type goSide struct {
	TxRawHex        string      `json:"txRawHex"`
	Version         uint32      `json:"version"`
	InputsTotalSat  uint64      `json:"inputsTotalSat"`
	OutputsTotalSat uint64      `json:"outputsTotalSat"`
	ImpliedFeeSat   uint64      `json:"impliedFeeSat"`
	Outputs         []outputRow `json:"outputs"`
}

type utxoForJS struct {
	TxID         string `json:"txId"`
	OutputIndex  uint32 `json:"outputIndex"`
	Satoshis     uint64 `json:"satoshis"`
	ScriptHex    string `json:"script"`
	APITxidEcho  string `json:"apiTxid"`
}

type outJSON struct {
	Meta struct {
		Mode         string  `json:"mode,omitempty"`
		Network      string  `json:"network"`
		LockTime     uint32  `json:"lockTime"`
		TbcNumber    float64 `json:"tbcNumber"`
		FetchAddr    string  `json:"fetchUtxoAddress"`
		FetchAmount  float64 `json:"fetchAmountTbc"`
		ExtraTbc     float64 `json:"extraTbc"`
		FTFeePerKB   string  `json:"ftFeeSatPerKbEnv,omitempty"`
	} `json:"meta"`
	UtxoForJS utxoForJS `json:"utxoForJs"`
	Go        goSide    `json:"go"`
}

// fixedUTXOFromEnv 当 PIGGY_USE_FIXED_UTXO=1 时，从环境变量构造 UTXO（不调用 FetchUTXO），用于与 JS 固定输入对照。
func fixedUTXOFromEnv() (*bt.UTXO, string, error) {
	txidHex := strings.ToLower(strings.TrimSpace(os.Getenv("PIGGY_FIXED_UTXO_TXID")))
	voutStr := strings.TrimSpace(os.Getenv("PIGGY_FIXED_UTXO_VOUT"))
	satStr := strings.TrimSpace(os.Getenv("PIGGY_FIXED_UTXO_SATOSHIS"))
	scriptHex := strings.TrimSpace(os.Getenv("PIGGY_FIXED_UTXO_SCRIPT"))
	if txidHex == "" || voutStr == "" || satStr == "" || scriptHex == "" {
		return nil, "", fmt.Errorf("fixed UTXO: need PIGGY_FIXED_UTXO_TXID, PIGGY_FIXED_UTXO_VOUT, PIGGY_FIXED_UTXO_SATOSHIS, PIGGY_FIXED_UTXO_SCRIPT")
	}
	txidBytes, err := hex.DecodeString(txidHex)
	if err != nil || len(txidBytes) != 32 {
		return nil, "", fmt.Errorf("PIGGY_FIXED_UTXO_TXID: invalid hex")
	}
	vout64, err := strconv.ParseUint(voutStr, 10, 32)
	if err != nil {
		return nil, "", fmt.Errorf("PIGGY_FIXED_UTXO_VOUT: %w", err)
	}
	sat, err := strconv.ParseUint(satStr, 10, 64)
	if err != nil {
		return nil, "", fmt.Errorf("PIGGY_FIXED_UTXO_SATOSHIS: %w", err)
	}
	ls, err := bscript.NewFromHexString(scriptHex)
	if err != nil {
		return nil, "", fmt.Errorf("PIGGY_FIXED_UTXO_SCRIPT: %w", err)
	}
	return &bt.UTXO{
		TxID:          txidBytes,
		Vout:          uint32(vout64),
		Satoshis:      sat,
		LockingScript: ls,
	}, txidHex, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	wifStr := strings.TrimSpace(os.Getenv("PIGGY_TEST_WIF"))
	network := strings.TrimSpace(os.Getenv("PIGGY_TEST_NETWORK"))
	if network == "" {
		network = "testnet"
	}
	tbcNumStr := strings.TrimSpace(os.Getenv("PIGGY_TEST_TBC_NUMBER"))
	if tbcNumStr == "" {
		tbcNumStr = "0.001"
	}
	tbcNum, err := strconv.ParseFloat(tbcNumStr, 64)
	if err != nil {
		return fmt.Errorf("PIGGY_TEST_TBC_NUMBER: %w", err)
	}
	extraStr := strings.TrimSpace(os.Getenv("PIGGY_FETCH_UTXO_EXTRA_TBC"))
	if extraStr == "" {
		extraStr = "0.1"
	}
	extraTbc, err := strconv.ParseFloat(extraStr, 64)
	if err != nil || extraTbc < 0 {
		return fmt.Errorf("PIGGY_FETCH_UTXO_EXTRA_TBC: %w", err)
	}
	useFixed := strings.TrimSpace(os.Getenv("PIGGY_USE_FIXED_UTXO")) == "1"
	lockStr := strings.TrimSpace(os.Getenv("PIGGY_TEST_LOCK_TIME"))
	var lockTime uint32
	if lockStr == "" {
		if useFixed {
			return fmt.Errorf("PIGGY_USE_FIXED_UTXO=1 时必须设置 PIGGY_TEST_LOCK_TIME（脚本内 uint32 锁定高度）")
		}
		headers, err := api.FetchBlockHeaders(network)
		if err != nil || len(headers) == 0 {
			return fmt.Errorf("PIGGY_TEST_LOCK_TIME 未设置且 FetchBlockHeaders 失败: %w", err)
		}
		h := headers[0].Height
		if h < 0 || h > 0x7fffffff {
			return fmt.Errorf("invalid tip height %d", h)
		}
		lockTime = uint32(h)
	} else {
		lockTime64, err := strconv.ParseUint(lockStr, 10, 32)
		if err != nil {
			return fmt.Errorf("PIGGY_TEST_LOCK_TIME: %w", err)
		}
		lockTime = uint32(lockTime64)
	}
	if wifStr == "" {
		return fmt.Errorf("need PIGGY_TEST_WIF")
	}
	dec, err := wif.DecodeWIF(wifStr)
	if err != nil {
		return fmt.Errorf("wif: %w", err)
	}
	priv := dec.PrivKey
	var utxo *bt.UTXO
	var apiTxid string
	var fetchAddr string
	var fetchAmount float64
	if useFixed {
		u, tid, err := fixedUTXOFromEnv()
		if err != nil {
			return err
		}
		utxo, apiTxid = u, tid
		fetchAddr = ""
		fetchAmount = 0
		extraTbc = 0
	} else {
		fetchAddr = strings.TrimSpace(os.Getenv("PIGGY_FETCH_UTXO_ADDRESS"))
		if fetchAddr == "" {
			fetchAddr = piggyDefaultFetchUTXOAddress
		}
		fetchAmount = tbcNum + extraTbc
		var err error
		utxo, apiTxid, err = api.FetchUTXOWithAPITxID(fetchAddr, fetchAmount, network)
		if err != nil {
			return fmt.Errorf("FetchUTXO: %w", err)
		}
	}
	if v := strings.TrimSpace(os.Getenv("FT_FEE_SAT_PER_KB")); v == "" {
		_ = os.Setenv("FT_FEE_SAT_PER_KB", "500")
	}
	raw, err := contract.FreezeTBCWithSign(priv, tbcNum, lockTime, []*bt.UTXO{utxo}, network)
	if err != nil {
		return fmt.Errorf("FreezeTBCWithSign: %w", err)
	}
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return fmt.Errorf("parse signed tx: %w", err)
	}
	var inSum uint64
	for _, u := range []*bt.UTXO{utxo} {
		inSum += u.Satoshis
	}
	var outSum uint64
	rows := make([]outputRow, 0, len(tx.Outputs))
	for i, o := range tx.Outputs {
		sh := ""
		if o.LockingScript != nil {
			sh = hex.EncodeToString(o.LockingScript.Bytes())
		}
		likely := len(sh) == 106
		outSum += o.Satoshis
		rows = append(rows, outputRow{
			Index:          i,
			Satoshis:       o.Satoshis,
			ScriptHexLen:   len(sh),
			ScriptHex:      sh,
			LikelyPiggyOut: likely,
		})
	}
	scriptHex := utxo.LockingScriptHexString()
	o := outJSON{}
	if useFixed {
		o.Meta.Mode = "fixed_utxo"
	}
	o.Meta.Network = network
	o.Meta.LockTime = lockTime
	o.Meta.TbcNumber = tbcNum
	o.Meta.FetchAddr = fetchAddr
	o.Meta.FetchAmount = fetchAmount
	o.Meta.ExtraTbc = extraTbc
	o.Meta.FTFeePerKB = strings.TrimSpace(os.Getenv("FT_FEE_SAT_PER_KB"))
	o.UtxoForJS = utxoForJS{
		TxID:        apiTxid,
		OutputIndex: utxo.Vout,
		Satoshis:    utxo.Satoshis,
		ScriptHex:   scriptHex,
		APITxidEcho: apiTxid,
	}
	o.Go = goSide{
		TxRawHex:        raw,
		Version:         tx.Version,
		InputsTotalSat:  inSum,
		OutputsTotalSat: outSum,
		ImpliedFeeSat:   inSum - outSum,
		Outputs:         rows,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(o)
}
