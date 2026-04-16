// piggy_unfreeze_compare：拉取 PIGGY_FREEZE_TXID 冻结交易，解析 Piggy UTXO，UnfreezeTBCWithSign，stdout 输出 JSON；不广播。
// 环境变量：PIGGY_FREEZE_TXID、TBC_PRIVATE_KEY（或 TBC_PRIVKEY）、TBC_NETWORK（默认 testnet）、
// PIGGY_FETCH_UTXO_ADDRESS（可选，默认 1P2to…，须与构造冻结时 GetPiggyBankCode 所用地址一致）。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/contract"
)

const piggyDefaultFetchAddress = "1P2toD4aKcUsxhTCbUjz5mcd3ajAJB9G1W"

type outputRow struct {
	Index        int    `json:"index"`
	Satoshis     uint64 `json:"satoshis"`
	ScriptHexLen int    `json:"scriptHexLen"`
	ScriptHex    string `json:"scriptHex,omitempty"`
}

type goSide struct {
	TxRawHex        string      `json:"txRawHex"`
	Version         uint32      `json:"version"`
	LockTime        uint32      `json:"lockTime"`
	InputsTotalSat  uint64      `json:"inputsTotalSat"`
	OutputsTotalSat uint64      `json:"outputsTotalSat"`
	ImpliedFeeSat   uint64      `json:"impliedFeeSat"`
	Outputs         []outputRow `json:"outputs"`
}

type utxoForJS struct {
	TxID        string `json:"txId"`
	OutputIndex uint32 `json:"outputIndex"`
	Satoshis    uint64 `json:"satoshis"`
	ScriptHex   string `json:"script"`
}

type outJSON struct {
	Meta struct {
		Network       string `json:"network"`
		FreezeTxid    string `json:"freezeTxid"`
		FetchAddrUsed string `json:"piggyFetchUtxoAddress"`
	} `json:"meta"`
	UtxoForJS utxoForJS `json:"utxoForJs"`
	Go        goSide    `json:"go"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	freezeTxid := strings.TrimSpace(os.Getenv("PIGGY_FREEZE_TXID"))
	if freezeTxid == "" {
		return fmt.Errorf("need PIGGY_FREEZE_TXID")
	}
	wifStr := strings.TrimSpace(os.Getenv("TBC_PRIVATE_KEY"))
	if wifStr == "" {
		wifStr = strings.TrimSpace(os.Getenv("TBC_PRIVKEY"))
	}
	if wifStr == "" {
		return fmt.Errorf("need TBC_PRIVATE_KEY or TBC_PRIVKEY")
	}
	network := strings.TrimSpace(os.Getenv("TBC_NETWORK"))
	if network == "" {
		network = "testnet"
	}
	addrStr := strings.TrimSpace(os.Getenv("PIGGY_FETCH_UTXO_ADDRESS"))
	if addrStr == "" {
		addrStr = piggyDefaultFetchAddress
	}
	dec, err := wif.DecodeWIF(wifStr)
	if err != nil {
		return fmt.Errorf("wif: %w", err)
	}
	priv := dec.PrivKey

	freezeTx, err := api.FetchTXRaw(freezeTxid, network)
	if err != nil {
		return fmt.Errorf("FetchTXRaw: %w", err)
	}
	piggyUtxo, err := contract.FindPiggyBankUTXOFromFreezeTx(freezeTxid, freezeTx, addrStr)
	if err != nil {
		return err
	}

	raw, err := contract.UnfreezeTBCWithSign(priv, []*bt.UTXO{piggyUtxo}, network)
	if err != nil {
		return fmt.Errorf("UnfreezeTBCWithSign: %w", err)
	}
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return fmt.Errorf("parse signed tx: %w", err)
	}

	var inSum uint64
	inSum = piggyUtxo.Satoshis
	var outSum uint64
	rows := make([]outputRow, 0, len(tx.Outputs))
	for i, o := range tx.Outputs {
		sh := ""
		if o.LockingScript != nil {
			sh = o.LockingScript.String()
		}
		outSum += o.Satoshis
		rows = append(rows, outputRow{
			Index:        i,
			Satoshis:     o.Satoshis,
			ScriptHexLen: len(sh),
			ScriptHex:    sh,
		})
	}

	o := outJSON{}
	o.Meta.Network = network
	o.Meta.FreezeTxid = strings.ToLower(freezeTxid)
	o.Meta.FetchAddrUsed = addrStr
	o.UtxoForJS = utxoForJS{
		TxID:        strings.ToLower(strings.TrimSpace(freezeTxid)),
		OutputIndex: piggyUtxo.Vout,
		Satoshis:    piggyUtxo.Satoshis,
		ScriptHex:   piggyUtxo.LockingScriptHexString(),
	}
	o.Go = goSide{
		TxRawHex:        raw,
		Version:         tx.Version,
		LockTime:        tx.LockTime,
		InputsTotalSat:  inSum,
		OutputsTotalSat: outSum,
		ImpliedFeeSat:   inSum - outSum,
		Outputs:         rows,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(o)
}
