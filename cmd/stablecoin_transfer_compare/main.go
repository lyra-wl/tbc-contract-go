// stablecoin_transfer_compare：与 scripts/stablecoin_transfer_test.ts 同参组包稳定币 transfer，stdout 输出 raw hex（不广播）。
//
// 与 ft_transfer_compare 类似，用于与 JS 逐字节对照；默认强制 FT_FEE_SAT_PER_KB=80（与 stableCoin.transfer feePerKb(80) 一致），
// 除非设 STABLECOIN_COMPARE_RESPECT_FEE_ENV=1。
//
// 环境变量：
//
//	TBC_PRIVATE_KEY（必填）
//	TBC_NETWORK（默认 testnet）
//	STABLECOIN_MINT_TXID（默认 ee05bf9e…）
//	STABLECOIN_NFT_TXID（可选；不设则从 mint input0 推导）
//	COIN_TRANSFER_TO（默认 1JdVc3…）
//	COIN_TRANSFER_AMOUNT（默认 1000）
//	STABLECOIN_LOCKSTEP_FEE_TXID / STABLECOIN_LOCKSTEP_FEE_VOUT（可选，与 JS 共用同一费 UTXO）
//
// 仅一行 hex：STABLECOIN_RAW_HEX_ONLY=1 go run ./cmd/stablecoin_transfer_compare
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/contract"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

func envOr(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v != "" {
		return v
	}
	return def
}

func mustNetwork() string {
	raw := strings.TrimSpace(os.Getenv("TBC_API_BASE"))
	if raw != "" {
		if !strings.HasSuffix(raw, "/") {
			raw += "/"
		}
		return raw
	}
	return envOr("TBC_NETWORK", "testnet")
}

func main() {
	if os.Getenv("STABLECOIN_COMPARE_RESPECT_FEE_ENV") != "1" {
		_ = os.Setenv("FT_FEE_SAT_PER_KB", "80")
	}

	wifStr := strings.TrimSpace(os.Getenv("TBC_PRIVATE_KEY"))
	if wifStr == "" {
		fmt.Println(`{"ok":"0","error":"missing TBC_PRIVATE_KEY"}`)
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(wifStr)
	if err != nil {
		fmt.Printf("%s\n", `{"ok":"0","error":"bad WIF"}`)
		os.Exit(1)
	}
	privKey := dec.PrivKey

	network := mustNetwork()
	mintTxid := envOr("STABLECOIN_MINT_TXID", "ee05bf9e4ca48cd499563b26c5ceb20d84e0defda3c6c3e170568c37add35cd0")
	nftEnv := strings.TrimSpace(os.Getenv("STABLECOIN_NFT_TXID"))
	nftID := nftEnv
	if nftID == "" {
		var err0 error
		nftID, err0 = api.StableCoinIndexerIDFromMintContractTx(mintTxid, network)
		if err0 != nil {
			b, _ := json.Marshal(map[string]string{"ok": "0", "error": err0.Error()})
			fmt.Println(string(b))
			os.Exit(1)
		}
	}

	si, err := api.FetchStableCoinInfo(nftID, network)
	if err != nil {
		b, _ := json.Marshal(map[string]string{"ok": "0", "error": "FetchStableCoinInfo: " + err.Error()})
		fmt.Println(string(b))
		os.Exit(1)
	}
	ts, ok := new(big.Int).SetString(strings.TrimSpace(si.TotalSupply), 10)
	var tsInt int64
	if ok && ts.IsInt64() {
		tsInt = ts.Int64()
	}
	sc, err := contract.NewStableCoin(mintTxid)
	if err != nil {
		fmt.Printf("%s\n", `{"ok":"0","error":"NewStableCoin"}`)
		os.Exit(1)
	}
	sc.Initialize(&contract.FtInfo{
		Name:        si.Name,
		Symbol:      si.Symbol,
		Decimal:     int(si.Decimal),
		TotalSupply: tsInt,
		CodeScript:  si.CodeScript,
		TapeScript:  si.TapeScript,
	})

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		fmt.Printf("%s\n", `{"ok":"0","error":"address"}`)
		os.Exit(1)
	}
	from := addr.AddressString
	to := envOr("COIN_TRANSFER_TO", "1JdVc3djVYG7GAMYAd1q9jkpp8gVycTDDq")
	amt := envOr("COIN_TRANSFER_AMOUNT", "1000")

	amountBN := util.ParseDecimalToBigInt(amt, sc.Decimal)
	codeHex := hex.EncodeToString(contract.BuildFTtransferCode(sc.CodeScript, from).Bytes())
	ftutxos, err := api.FetchFtUTXOs(mintTxid, from, codeHex, network, amountBN)
	if err != nil {
		b, _ := json.Marshal(map[string]string{"ok": "0", "error": "FetchFtUTXOs: " + err.Error()})
		fmt.Println(string(b))
		os.Exit(1)
	}

	preTXs := make([]*bt.Tx, len(ftutxos))
	prepre := make([]string, len(ftutxos))
	for i := range ftutxos {
		preTXs[i], err = api.FetchTXRaw(ftutxos[i].TxID, network)
		if err != nil {
			fmt.Printf("%s\n", `{"ok":"0","error":"FetchTXRaw"}`)
			os.Exit(1)
		}
		prepre[i], err = api.FetchFtPrePreTxData(preTXs[i], int(ftutxos[i].Vout), network)
		if err != nil {
			fmt.Printf("%s\n", `{"ok":"0","error":"FetchFtPrePreTxData"}`)
			os.Exit(1)
		}
	}

	var feeUTXO *bt.UTXO
	ftx := strings.TrimSpace(os.Getenv("STABLECOIN_LOCKSTEP_FEE_TXID"))
	fv := strings.TrimSpace(os.Getenv("STABLECOIN_LOCKSTEP_FEE_VOUT"))
	if ftx != "" && fv != "" {
		vout, _ := strconv.Atoi(fv)
		feeTx, err := api.FetchTXRaw(ftx, network)
		if err != nil || vout < 0 || vout >= len(feeTx.Outputs) {
			fmt.Printf("%s\n", `{"ok":"0","error":"lockstep fee tx"}`)
			os.Exit(1)
		}
		txidBytes, _ := hex.DecodeString(ftx)
		feeUTXO = &bt.UTXO{
			TxID:          txidBytes,
			Vout:          uint32(vout),
			Satoshis:      feeTx.Outputs[vout].Satoshis,
			LockingScript: feeTx.Outputs[vout].LockingScript,
		}
	} else {
		feeUTXO, err = api.FetchUTXO(from, 0.02, network)
		if err != nil {
			b, _ := json.Marshal(map[string]string{"ok": "0", "error": "FetchUTXO: " + err.Error()})
			fmt.Println(string(b))
			os.Exit(1)
		}
	}

	raw, err := sc.TransferCoin(privKey, to, amt, ftutxos, feeUTXO, preTXs, prepre, 0)
	if err != nil {
		b, _ := json.Marshal(map[string]string{"ok": "0", "error": "TransferCoin: " + err.Error()})
		fmt.Println(string(b))
		os.Exit(1)
	}

	if strings.TrimSpace(os.Getenv("STABLECOIN_RAW_HEX_ONLY")) == "1" {
		fmt.Println(strings.ToLower(raw))
		return
	}
	tx, _ := bt.NewTxFromString(raw)
	b, _ := json.Marshal(map[string]string{"ok": "1", "txid": tx.TxID(), "raw_hex": raw})
	fmt.Println(string(b))
}
