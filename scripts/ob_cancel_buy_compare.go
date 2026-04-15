//go:build ignore
// +build ignore

// OrderBook CancelBuyOrder 对照脚本 — 生成 Go 端的 FT unlock 各子组件 hex
// 用于与 TS 端逐字节比对。
//
// 用法：
//   cd tbc-contract-go
//   export TBC_PRIVATE_KEY='L1u2TmR7hMMMSV9Bx2Lyt3sujbboqEFqnKygnPRnQERhKB4qptuK'
//   export OB_BUY_ORDER_TXID='...'
//   go run scripts/ob_cancel_buy_compare.go

package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/contract"
)

func mustEnv(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		fmt.Fprintf(os.Stderr, "Missing env: %s\n", key)
		os.Exit(1)
	}
	return v
}

func main() {
	privKeyWIF := mustEnv("TBC_PRIVATE_KEY")
	buyOrderTxid := mustEnv("OB_BUY_ORDER_TXID")
	network := os.Getenv("TBC_NETWORK")
	if network == "" {
		network = "testnet"
	}

	decodedWif, err := wif.DecodeWIF(privKeyWIF)
	if err != nil {
		panic(err)
	}
	privKey := decodedWif.PrivKey

	addr, _ := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	fmt.Println("=== Go CancelBuyOrder Component Comparison ===")
	fmt.Println("Address:", addr.AddressString)
	fmt.Println("BuyOrderTxid:", buyOrderTxid)

	buyPreTX, err := api.FetchTXRaw(buyOrderTxid, network)
	if err != nil {
		panic(err)
	}
	fmt.Printf("buyPreTX inputs: %d outputs: %d\n", len(buyPreTX.Inputs), len(buyPreTX.Outputs))

	// Build UTXOs
	txidBytes, _ := hex.DecodeString(buyOrderTxid)
	buyUTXO := &bt.UTXO{
		TxID:          txidBytes,
		Vout:          0,
		Satoshis:      buyPreTX.Outputs[0].Satoshis,
		LockingScript: buyPreTX.Outputs[0].LockingScript,
	}
	ftUTXO := &bt.UTXO{
		TxID:          txidBytes,
		Vout:          1,
		Satoshis:      buyPreTX.Outputs[1].Satoshis,
		LockingScript: buyPreTX.Outputs[1].LockingScript,
	}

	fmt.Printf("buyUTXO script len: %d\n", len(buyUTXO.LockingScript.Bytes()))
	fmt.Printf("ftUTXO script len: %d\n", len(ftUTXO.LockingScript.Bytes()))

	// Extract FT balance from tape (vout=2, i.e. ft vout+1)
	tapeScript := buyPreTX.Outputs[2].LockingScript.Bytes()
	ftBalance := contract.GetBalanceFromTape(hex.EncodeToString(tapeScript))
	if ftBalance == nil {
		ftBalance = big.NewInt(0)
	}
	fmt.Println("ftBalance:", ftBalance.String())

	buyData, err := contract.GetOrderData(buyUTXO.LockingScript.String(), true)
	if err != nil {
		panic(err)
	}
	fmt.Println("holdAddress:", buyData.HoldAddress)
	fmt.Println("ftID:", buyData.FtID)

	ftPreTX := buyPreTX
	ftPrePreTxData, err := api.FetchFtPrePreTxData(ftPreTX, 1, network)
	if err != nil {
		panic(err)
	}

	// Fetch fee UTXO
	feeUTXO, err := api.FetchUTXO(addr.AddressString, 0.01, network)
	if err != nil {
		panic(err)
	}

	// Build transaction
	tx := contract.NewFTTxExported()
	_ = tx.FromUTXOs(buyUTXO)
	_ = tx.FromUTXOs(ftUTXO)
	_ = tx.FromUTXOs(feeUTXO)

	tapeAmountSetIn := []*big.Int{ftBalance}
	amountHex, _ := contract.BuildTapeAmountWithFtInputIndex(ftBalance, tapeAmountSetIn, 1)

	ftCodeHex := ftUTXO.LockingScript.String()
	ftTapeHex := hex.EncodeToString(ftPreTX.Outputs[2].LockingScript.Bytes())

	ftCodeOut := contract.BuildFTtransferCode(ftCodeHex, buyData.HoldAddress)
	ftTapeOut := contract.BuildFTtransferTape(ftTapeHex, amountHex)

	tx.AddOutput(&bt.Output{LockingScript: ftCodeOut, Satoshis: ftUTXO.Satoshis})
	tx.AddOutput(&bt.Output{LockingScript: ftTapeOut, Satoshis: 0})

	// Change output
	feeInputTotal := feeUTXO.Satoshis
	fee := contract.ObCalcFeeExported(contract.TbcJSEstimateTxBytesExported(tx) + 2000 + 34)
	if int(feeInputTotal) > fee {
		tx.To(buyData.HoldAddress, uint64(int(feeInputTotal)-fee))
	}

	fmt.Printf("\n=== TX Structure (before signing) ===\n")
	fmt.Printf("inputs: %d\n", len(tx.Inputs))
	fmt.Printf("outputs: %d\n", len(tx.Outputs))
	for i, o := range tx.Outputs {
		fmt.Printf("  out[%d]: satoshis=%d, scriptLen=%d\n", i, o.Satoshis, len(o.LockingScript.Bytes()))
	}
	fmt.Printf("  feeUTXO: %s %d %d\n", hex.EncodeToString(feeUTXO.TxID), feeUTXO.Vout, feeUTXO.Satoshis)

	// ===== Dump individual FT unlock swap components (for input 1) =====

	// GetCurrentTxdata (ftunlock version)
	currentTxdataFT, err := bt.GetCurrentTxdata(tx, 1)
	if err != nil {
		panic(fmt.Sprintf("GetCurrentTxdata: %v", err))
	}
	fmt.Printf("\n=== FT Unlock Components (input 1 - FT UTXO) ===\n")
	fmt.Printf("[currentTxdata] len=%d\n", len(currentTxdataFT)/2)
	fmt.Println("[currentTxdata]", currentTxdataFT)

	// GetPreTxdata (ftunlock version)
	preTxdataFT, err := bt.GetPreTxdata(ftPreTX, int(ftUTXO.Vout))
	if err != nil {
		panic(fmt.Sprintf("GetPreTxdata: %v", err))
	}
	fmt.Printf("[preTxdata] len=%d\n", len(preTxdataFT)/2)
	fmt.Println("[preTxdata]", preTxdataFT)

	// GetContractTxdata - contractTX = buyPreTX, ftVersion=2 means vout=-1
	contractTxdataFT, err := bt.GetContractTxdata(buyPreTX, -1)
	if err != nil {
		panic(fmt.Sprintf("GetContractTxdata: %v", err))
	}
	fmt.Printf("[contractTxdata] len=%d\n", len(contractTxdataFT)/2)
	fmt.Println("[contractTxdata]", contractTxdataFT)

	// GetCurrentInputsdata
	currentInputsdataFT := bt.GetCurrentInputsdata(tx)
	fmt.Printf("[currentInputsdata] len=%d\n", len(currentInputsdataFT)/2)
	fmt.Println("[currentInputsdata]", currentInputsdataFT)

	// prepreTxData (fetched from API)
	fmt.Printf("[prepreTxData] len=%d\n", len(ftPrePreTxData)/2)
	fmt.Println("[prepreTxData]", ftPrePreTxData)

	// ===== Dump orderbook unlock components (for input 0 - buy order) =====
	obCurrentTxOutputsData, err := contract.GetOrderBookCurrentTxOutputsDataExported(tx)
	if err != nil {
		panic(err)
	}
	obPreTxdata, err := contract.GetOrderBookPreTxdataExported(buyPreTX, 0, 1)
	if err != nil {
		panic(err)
	}
	fmt.Printf("\n=== OrderBook Unlock Components (input 0 - buy order) ===\n")
	fmt.Printf("[ob_currentTxOutputsData] len=%d\n", len(obCurrentTxOutputsData)/2)
	fmt.Println("[ob_currentTxOutputsData]", obCurrentTxOutputsData)
	fmt.Printf("[ob_preTxdata] len=%d\n", len(obPreTxdata)/2)
	fmt.Println("[ob_preTxdata]", obPreTxdata)

	// Print some debugging info about inputs
	fmt.Printf("\n=== Input Details ===\n")
	for i, in := range tx.Inputs {
		prevID := in.PreviousTxID()
		reversed := make([]byte, 32)
		for j := 0; j < 32; j++ {
			reversed[j] = prevID[31-j]
		}
		fmt.Printf("  input[%d]: prevTxID=%s, prevOutIdx=%d, seq=%d\n",
			i, hex.EncodeToString(reversed), in.PreviousTxOutIndex, in.SequenceNumber)
	}

	// Print output locking script hashes (for debugging contract txdata)
	fmt.Printf("\n=== Output Script Hashes ===\n")
	for i, o := range buyPreTX.Outputs {
		script := o.LockingScript.Bytes()
		sat := make([]byte, 8)
		binary.LittleEndian.PutUint64(sat, o.Satoshis)
		fmt.Printf("  buyPreTX out[%d]: satoshis=%d, scriptLen=%d\n", i, o.Satoshis, len(script))
	}
}
