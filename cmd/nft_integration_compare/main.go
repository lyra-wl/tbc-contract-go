// nft_integration_compare：读取 NFTC_INTEGRATION_FIXTURE_PATH 指向的 fixture JSON，
// 依次调用 contract.CreateCollection、CreateNFT、(*NFT).TransferNFT，将各步原始交易 hex 与摘要 JSON 打印到 stdout；不广播。
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

type utxoIn struct {
	Txid     string `json:"txid"`
	Vout     uint32 `json:"vout"`
	Satoshis uint64 `json:"satoshis"`
	Script   string `json:"script"`
}

type fixtureInputs struct {
	PrivateKeyWIF    string `json:"private_key_wif"`
	Network          string `json:"network"`
	AddressMintSlots string `json:"address_mint_slots"`
	AddressNftOwner  string `json:"address_nft_owner"`
	TransferTo       string `json:"transfer_to"`
	CollectionData   struct {
		CollectionName string `json:"collectionName"`
		Name           string `json:"name"`
		Description    string `json:"description"`
		Supply         int    `json:"supply"`
		File           string `json:"file"`
	} `json:"collection_data"`
	NftMintData struct {
		NftName     string `json:"nftName"`
		Symbol      string `json:"symbol"`
		Description string `json:"description"`
		Attributes  string `json:"attributes"`
		File        string `json:"file"`
	} `json:"nft_mint_data"`
	MintSlotVout uint32 `json:"mint_slot_vout"`
	UTXOCol      utxoIn `json:"utxo_collection"`
	UTXOMintFee  utxoIn `json:"utxo_mint_fee"`
	UTXOXferFee  utxoIn `json:"utxo_transfer_fee"`
	NFTInfoMeta  struct {
		CollectionName          string `json:"collection_name"`
		NftName                 string `json:"nft_name"`
		NftSymbol               string `json:"nft_symbol"`
		NftAttributes           string `json:"nft_attributes"`
		NftDescription          string `json:"nft_description"`
		NftTransferTimeCount    int    `json:"nft_transfer_time_count"`
		NftIcon                 string `json:"nft_icon"`
	} `json:"nft_info_after_mint"`
}

type fixtureDoc struct {
	TestID   string        `json:"test_id"`
	Disabled bool          `json:"disabled"`
	Inputs   fixtureInputs `json:"inputs"`
}

type outputRow struct {
	Index        int    `json:"index"`
	Satoshis     uint64 `json:"satoshis"`
	ScriptHexLen int    `json:"scriptHexLen"`
	ScriptHex    string `json:"scriptHex,omitempty"`
	OutputKind   string `json:"outputKind,omitempty"`
}

type txSide struct {
	TxRawHex        string      `json:"txRawHex"`
	Version         uint32      `json:"version"`
	InputsTotalSat  uint64      `json:"inputsTotalSat"`
	OutputsTotalSat uint64      `json:"outputsTotalSat"`
	ImpliedFeeSat   uint64      `json:"impliedFeeSat"`
	Outputs         []outputRow `json:"outputs"`
}

type stepOut struct {
	Go txSide `json:"go"`
}

type outJSON struct {
	Meta struct {
		Network        string `json:"network"`
		FixtureTestID  string `json:"fixtureTestId"`
		FTFeeSatPerKB  string `json:"ftFeeSatPerKbEnv,omitempty"`
		NFTFeeSatPerKB string `json:"nftFeeSatPerKbEnv,omitempty"`
	} `json:"meta"`
	CollectionTxID string  `json:"collectionTxId"`
	MintTxID       string  `json:"mintTxId"`
	CreateCollection stepOut `json:"createCollection"`
	CreateNFT        stepOut `json:"createNFT"`
	TransferNFT      stepOut `json:"transferNFT"`
}

func outputKind(i, nOut int, sat uint64, scriptHex string) string {
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

func summarizeTx(tx *bt.Tx, inputSum uint64) txSide {
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
	return txSide{
		TxRawHex:        hex.EncodeToString(tx.Bytes()),
		Version:         tx.Version,
		InputsTotalSat:  inputSum,
		OutputsTotalSat: outSum,
		ImpliedFeeSat:   inputSum - outSum,
		Outputs:         rows,
	}
}

func parseUTXO(u utxoIn) (*bt.UTXO, error) {
	txidHex := strings.ToLower(strings.TrimSpace(u.Txid))
	txidBytes, err := hex.DecodeString(txidHex)
	if err != nil || len(txidBytes) != 32 {
		return nil, fmt.Errorf("utxo txid invalid")
	}
	ls, err := bscript.NewFromHexString(strings.TrimSpace(u.Script))
	if err != nil {
		return nil, err
	}
	return &bt.UTXO{
		TxID:          txidBytes,
		Vout:          u.Vout,
		LockingScript: ls,
		Satoshis:      u.Satoshis,
	}, nil
}

func sumInputs(ux []*bt.UTXO) uint64 {
	var s uint64
	for _, u := range ux {
		s += u.Satoshis
	}
	return s
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fixPath := strings.TrimSpace(os.Getenv("NFTC_INTEGRATION_FIXTURE_PATH"))
	if fixPath == "" {
		return fmt.Errorf("need NFTC_INTEGRATION_FIXTURE_PATH")
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
	wifStr := strings.TrimSpace(in.PrivateKeyWIF)
	if wifStr == "" {
		return fmt.Errorf("private_key_wif required")
	}
	dec, err := wif.DecodeWIF(wifStr)
	if err != nil {
		return fmt.Errorf("wif: %w", err)
	}
	priv := dec.PrivKey

	utxoCol, err := parseUTXO(in.UTXOCol)
	if err != nil {
		return fmt.Errorf("utxo_collection: %w", err)
	}
	utxoMintFee, err := parseUTXO(in.UTXOMintFee)
	if err != nil {
		return fmt.Errorf("utxo_mint_fee: %w", err)
	}
	utxoXferFee, err := parseUTXO(in.UTXOXferFee)
	if err != nil {
		return fmt.Errorf("utxo_transfer_fee: %w", err)
	}

	mintAddr := strings.TrimSpace(in.AddressMintSlots)
	ownerAddr := strings.TrimSpace(in.AddressNftOwner)
	if mintAddr == "" || ownerAddr == "" {
		return fmt.Errorf("address_mint_slots and address_nft_owner required")
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
	if cd.Supply < 1 {
		return fmt.Errorf("invalid supply")
	}

	vMint := in.MintSlotVout
	if vMint == 0 {
		vMint = 1
	}
	if int(vMint) >= 1+cd.Supply {
		return fmt.Errorf("mint_slot_vout out of range")
	}

	rawCol, err := contract.CreateCollection(mintAddr, priv, cd, []*bt.UTXO{utxoCol})
	if err != nil {
		return fmt.Errorf("CreateCollection: %w", err)
	}
	colTx, err := bt.NewTxFromString(rawCol)
	if err != nil {
		return fmt.Errorf("parse collection tx: %w", err)
	}
	colTxID := colTx.TxID()
	if int(vMint) >= len(colTx.Outputs) {
		return fmt.Errorf("collection tx missing output %d", vMint)
	}
	mintOut := colTx.Outputs[vMint]
	mintUtxo := &bt.UTXO{
		TxID:          append([]byte(nil), colTx.TxIDBytes()...),
		Vout:          vMint,
		LockingScript: mintOut.LockingScript,
		Satoshis:      mintOut.Satoshis,
	}

	nftData := &contract.NFTData{
		NftName:     in.NftMintData.NftName,
		Symbol:      in.NftMintData.Symbol,
		Description: in.NftMintData.Description,
		Attributes:  in.NftMintData.Attributes,
		File:        in.NftMintData.File,
	}
	rawNFT, err := contract.CreateNFT(colTxID, ownerAddr, priv, nftData, []*bt.UTXO{utxoMintFee}, mintUtxo)
	if err != nil {
		return fmt.Errorf("CreateNFT: %w", err)
	}
	mintTx, err := bt.NewTxFromString(rawNFT)
	if err != nil {
		return fmt.Errorf("parse mint tx: %w", err)
	}
	mintTxID := mintTx.TxID()

	toStr := strings.TrimSpace(in.TransferTo)
	if toStr == "" {
		toStr = ownerAddr
	}

	meta := in.NFTInfoMeta
	info := &contract.NFTInfo{
		CollectionID:         colTxID,
		CollectionIndex:      int(vMint),
		CollectionName:       meta.CollectionName,
		NftName:              meta.NftName,
		NftSymbol:            meta.NftSymbol,
		NftAttributes:        meta.NftAttributes,
		NftDescription:       meta.NftDescription,
		NftTransferTimeCount: meta.NftTransferTimeCount,
		NftIcon:              meta.NftIcon,
	}
	nft := contract.NewNFT(mintTxID)
	nft.Initialize(info)

	var prePre *bt.Tx
	if meta.NftTransferTimeCount == 0 {
		prePre = colTx
	} else {
		return fmt.Errorf("fixture only supports nft_transfer_time_count=0 for pre_pre_tx")
	}

	rawXfer, err := nft.TransferNFT(ownerAddr, toStr, priv, []*bt.UTXO{utxoXferFee}, mintTx, prePre, false)
	if err != nil {
		return fmt.Errorf("TransferNFT: %w", err)
	}
	xferTx, err := bt.NewTxFromString(rawXfer)
	if err != nil {
		return fmt.Errorf("parse transfer tx: %w", err)
	}

	o := outJSON{}
	network := strings.TrimSpace(in.Network)
	if network == "" {
		network = "testnet"
	}
	o.Meta.Network = network
	o.Meta.FixtureTestID = doc.TestID
	o.Meta.FTFeeSatPerKB = strings.TrimSpace(os.Getenv("FT_FEE_SAT_PER_KB"))
	o.Meta.NFTFeeSatPerKB = strings.TrimSpace(os.Getenv("NFT_FEE_SAT_PER_KB"))
	o.CollectionTxID = colTxID
	o.MintTxID = mintTxID

	inSumCol := sumInputs([]*bt.UTXO{utxoCol})
	o.CreateCollection.Go = summarizeTx(colTx, inSumCol)

	inSumNFT := mintUtxo.Satoshis + utxoMintFee.Satoshis
	o.CreateNFT.Go = summarizeTx(mintTx, inSumNFT)

	inSumXfer := mintTx.Outputs[0].Satoshis + mintTx.Outputs[1].Satoshis + utxoXferFee.Satoshis
	o.TransferNFT.Go = summarizeTx(xferTx, inSumXfer)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(o)
}
