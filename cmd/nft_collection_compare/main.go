// nft_collection_compare：读取 fixture JSON（环境变量 NFTC_FIXTURE_PATH），调用 contract.CreateCollection，将 Go 侧摘要以 JSON 打印到 stdout；不广播。
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/contract"
)

type outputRow struct {
	Index        int    `json:"index"`
	Satoshis     uint64 `json:"satoshis"`
	ScriptHexLen int    `json:"scriptHexLen"`
	ScriptHex    string `json:"scriptHex,omitempty"`
	OutputKind   string `json:"outputKind,omitempty"`
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
	TxID        string `json:"txId"`
	OutputIndex uint32 `json:"outputIndex"`
	Satoshis    uint64 `json:"satoshis"`
	ScriptHex   string `json:"script"`
	APITxidEcho string `json:"apiTxid"`
}

type fixtureInputs struct {
	PrivateKeyWIF    string `json:"private_key_wif"`
	Network          string `json:"network"`
	AddressMintSlots string `json:"address_mint_slots"`
	CollectionData   struct {
		Name           string `json:"name"`
		CollectionName string `json:"collectionName"`
		Description    string `json:"description"`
		Supply         int    `json:"supply"`
		File           string `json:"file"`
	} `json:"collection_data"`
	UTXOs []struct {
		Txid     string `json:"txid"`
		Vout     uint32 `json:"vout"`
		Satoshis uint64 `json:"satoshis"`
		Script   string `json:"script"`
	} `json:"utxos"`
}

type fixtureDoc struct {
	TestID   string        `json:"test_id"`
	Disabled bool          `json:"disabled"`
	Inputs   fixtureInputs `json:"inputs"`
}

type outJSON struct {
	Meta struct {
		Mode           string `json:"mode,omitempty"`
		Network        string `json:"network"`
		MintSlotsAddr  string `json:"addressMintSlots"`
		Supply         int    `json:"supply"`
		FTFeeSatPerKB  string `json:"ftFeeSatPerKbEnv,omitempty"`
		NFTFeeSatPerKB string `json:"nftFeeSatPerKbEnv,omitempty"`
		FixtureTestID  string `json:"fixtureTestId,omitempty"`
	} `json:"meta"`
	UtxoForJS utxoForJS `json:"utxoForJs"`
	Go        goSide    `json:"go"`
}

func outputKind(i int, nOut int, sat uint64, scriptHex string) string {
	if sat == 0 && strings.HasPrefix(scriptHex, "006a") {
		return "tape"
	}
	if sat == 100 {
		return "mint_slot"
	}
	if i == nOut-1 {
		return "change"
	}
	return "other"
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fixPath := strings.TrimSpace(os.Getenv("NFTC_FIXTURE_PATH"))
	if fixPath == "" {
		return fmt.Errorf("need NFTC_FIXTURE_PATH to fixture json")
	}
	raw, err := os.ReadFile(filepath.Clean(fixPath))
	if err != nil {
		return fmt.Errorf("read fixture: %w", err)
	}
	var doc fixtureDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse fixture: %w", err)
	}
	if doc.Disabled {
		return fmt.Errorf("fixture disabled (test_id=%s)", doc.TestID)
	}
	in := doc.Inputs
	if len(in.UTXOs) < 1 {
		return fmt.Errorf("fixture.inputs.utxos required")
	}
	wifStr := strings.TrimSpace(in.PrivateKeyWIF)
	if wifStr == "" {
		return fmt.Errorf("private_key_wif required")
	}
	dec, err := wif.DecodeWIF(wifStr)
	if err != nil {
		return fmt.Errorf("wif: %w", err)
	}
	priv := dec.PrivKey
	u0 := in.UTXOs[0]
	txidHex := strings.ToLower(strings.TrimSpace(u0.Txid))
	txidBytes, err := hex.DecodeString(txidHex)
	if err != nil || len(txidBytes) != 32 {
		return fmt.Errorf("utxo txid: invalid hex")
	}
	ls, err := bscript.NewFromHexString(strings.TrimSpace(u0.Script))
	if err != nil {
		return fmt.Errorf("utxo script: %w", err)
	}
	utxo := &bt.UTXO{
		TxID:          txidBytes,
		Vout:          u0.Vout,
		Satoshis:      u0.Satoshis,
		LockingScript: ls,
	}
	cn := strings.TrimSpace(in.CollectionData.CollectionName)
	if cn == "" {
		cn = strings.TrimSpace(in.CollectionData.Name)
	}
	cd := &contract.CollectionData{
		CollectionName: cn,
		Description:    in.CollectionData.Description,
		Supply:         in.CollectionData.Supply,
		File:           in.CollectionData.File,
	}
	mintAddr := strings.TrimSpace(in.AddressMintSlots)
	if mintAddr == "" {
		return fmt.Errorf("address_mint_slots required")
	}
	network := strings.TrimSpace(in.Network)
	if network == "" {
		network = "testnet"
	}

	rawHex, err := contract.CreateCollection(mintAddr, priv, cd, []*bt.UTXO{utxo})
	if err != nil {
		return fmt.Errorf("CreateCollection: %w", err)
	}
	tx, err := bt.NewTxFromString(rawHex)
	if err != nil {
		return fmt.Errorf("parse signed tx: %w", err)
	}

	var inSum uint64
	for _, u := range []*bt.UTXO{utxo} {
		inSum += u.Satoshis
	}
	var outSum uint64
	nOut := len(tx.Outputs)
	rows := make([]outputRow, 0, nOut)
	for i, o := range tx.Outputs {
		sh := ""
		if o.LockingScript != nil {
			sh = hex.EncodeToString(o.LockingScript.Bytes())
		}
		outSum += o.Satoshis
		rows = append(rows, outputRow{
			Index:        i,
			Satoshis:     o.Satoshis,
			ScriptHexLen: len(sh),
			ScriptHex:    sh,
			OutputKind:   outputKind(i, nOut, o.Satoshis, sh),
		})
	}
	scriptHex := utxo.LockingScriptHexString()
	o := outJSON{}
	o.Meta.Mode = "fixed_utxo"
	o.Meta.Network = network
	o.Meta.MintSlotsAddr = mintAddr
	o.Meta.Supply = cd.Supply
	o.Meta.FixtureTestID = doc.TestID
	o.Meta.FTFeeSatPerKB = strings.TrimSpace(os.Getenv("FT_FEE_SAT_PER_KB"))
	o.Meta.NFTFeeSatPerKB = strings.TrimSpace(os.Getenv("NFT_FEE_SAT_PER_KB"))
	o.UtxoForJS = utxoForJS{
		TxID:        txidHex,
		OutputIndex: u0.Vout,
		Satoshis:    u0.Satoshis,
		ScriptHex:   scriptHex,
		APITxidEcho: txidHex,
	}
	o.Go = goSide{
		TxRawHex:        rawHex,
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
