//go:build integration
// +build integration

// 基于 docs/stableCoin.md 的稳定币集成测试（含广播）。
//
// 运行：
//   cd ~/path/to/tbc-contract-go
//   export RUN_REAL_COIN_TEST=1
//   export TBC_PRIVATE_KEY=管理员WIF
//   export TBC_NETWORK=testnet
//   export COIN_TRANSFER_TO=接收地址
//   # 可选：COIN_CONTRACT_TXID=已有合约txid
//   go test -tags=integration -v ./lib/contract -run TestStableCoin -count=1
package contract

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/libsv/go-bk/bec"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

func requireRealCoinRun(t *testing.T) {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("RUN_REAL_COIN_TEST"))
	if raw != "1" {
		t.Skip("默认跳过真实链上稳定币测试，设置 RUN_REAL_COIN_TEST=1 启用")
	}
}

func loadCoinPrivKey(t *testing.T) *bec.PrivateKey {
	return loadPrivKey(t)
}

func loadStableCoinForIntegration(t *testing.T, network, contractTxid string) *StableCoin {
	t.Helper()
	sc, err := NewStableCoin(contractTxid)
	if err != nil {
		t.Fatalf("NewStableCoin: %v", err)
	}
	info, err := api.FetchFtInfo(contractTxid, network)
	if err != nil {
		t.Fatalf("FetchFtInfo(coin): %v", err)
	}
	totalSupply, ok := new(big.Int).SetString(info.TotalSupply, 10)
	if !ok {
		t.Fatalf("非法 TotalSupply: %s", info.TotalSupply)
	}
	sc.Initialize(&FtInfo{
		Name:        info.Name,
		Symbol:      info.Symbol,
		Decimal:     int(info.Decimal),
		TotalSupply: totalSupply.Int64(),
		CodeScript:  info.CodeScript,
		TapeScript:  info.TapeScript,
	})
	return sc
}

// TestStableCoin_Integration_CreateCoin 对应 stableCoin.md CreateCoin
func TestStableCoin_Integration_CreateCoin(t *testing.T) {
	requireRealCoinRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadCoinPrivKey(t)
	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成地址失败: %v", err)
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

	utxo, err := api.FetchUTXO(addr.AddressString, 0.02, network)
	if err != nil {
		t.Fatalf("FetchUTXO: %v", err)
	}

	utxoTX, err := api.FetchTXRaw(hex.EncodeToString(utxo.TxID), network)
	if err != nil {
		t.Fatalf("FetchTXRaw(utxo): %v", err)
	}

	txraws, err := sc.CreateCoin(privKey, addr.AddressString, utxo, utxoTX, "Integration Test Mint")
	if err != nil {
		t.Fatalf("CreateCoin: %v", err)
	}
	if len(txraws) != 2 {
		t.Fatalf("CreateCoin 返回交易数=%d，期望=2", len(txraws))
	}

	nftTxid, err := api.BroadcastTXRaw(txraws[0], network)
	if err != nil {
		t.Fatalf("广播 coinNFT 交易失败: %v", err)
	}
	t.Logf("CoinNFT txid=%s", nftTxid)

	waitTxVisible(t, network, nftTxid, 12, 1*time.Second)

	mintTxid, err := broadcastMintWithRetry(network, txraws[1], 8, 1*time.Second)
	if err != nil {
		t.Fatalf("广播 coinMint 交易失败: %v", err)
	}
	t.Logf("CoinMint txid=%s contractTxid=%s", mintTxid, sc.ContractTxid)
}

// TestStableCoin_Integration_Transfer 对应 stableCoin.md Transfer
func TestStableCoin_Integration_Transfer(t *testing.T) {
	requireRealCoinRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadCoinPrivKey(t)
	contractTxid := mustEnv(t, "COIN_CONTRACT_TXID")
	toAddress := mustEnvOrConst(t, "COIN_TRANSFER_TO", defaultTransferTo)
	transferAmount := envOrDefault("COIN_TRANSFER_AMOUNT", "1000")

	sc := loadStableCoinForIntegration(t, network, contractTxid)

	fromAddr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成来源地址失败: %v", err)
	}

	amountBN := util.ParseDecimalToBigInt(transferAmount, sc.Decimal)
	ftCodeScript := BuildFTtransferCode(sc.CodeScript, fromAddr.AddressString)
	ftutxos, err := api.FetchFtUTXOs(contractTxid, fromAddr.AddressString, hex.EncodeToString(ftCodeScript.Bytes()), network, amountBN)
	if err != nil {
		t.Fatalf("FetchFtUTXOs: %v", err)
	}
	if len(ftutxos) == 0 {
		t.Fatal("没有可用稳定币 UTXO")
	}

	preTXs := make([]*bt.Tx, len(ftutxos))
	prepreTxDatas := make([]string, len(ftutxos))
	for i := range ftutxos {
		preTXs[i], err = api.FetchTXRaw(ftutxos[i].TxID, network)
		if err != nil {
			t.Fatalf("FetchTXRaw(%s): %v", ftutxos[i].TxID, err)
		}
		prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(ftutxos[i].Vout), network)
		if err != nil {
			t.Fatalf("FetchFtPrePreTxData: %v", err)
		}
	}

	feeUTXO, err := api.FetchUTXO(fromAddr.AddressString, 0.01, network)
	if err != nil {
		t.Fatalf("FetchUTXO(fee): %v", err)
	}

	txraw, err := sc.TransferCoin(privKey, toAddress, transferAmount, ftutxos, feeUTXO, preTXs, prepreTxDatas, 0)
	if err != nil {
		t.Fatalf("TransferCoin: %v", err)
	}

	txid, err := api.BroadcastTXRaw(txraw, network)
	if err != nil {
		t.Fatalf("广播 transfer 交易失败: %v", err)
	}
	t.Logf("StableCoin Transfer 广播成功 txid=%s", txid)
}

// TestStableCoin_Integration_FreezeCoinUTXO 对应 stableCoin.md FreezeCoinUTXO
func TestStableCoin_Integration_FreezeCoinUTXO(t *testing.T) {
	requireRealCoinRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKeyAdmin := loadCoinPrivKey(t)
	contractTxid := mustEnv(t, "COIN_CONTRACT_TXID")
	targetAddress := mustEnv(t, "COIN_FREEZE_TARGET")
	lockTime := uint32(1774410989) // example lock time

	sc := loadStableCoinForIntegration(t, network, contractTxid)
	ftCodeScript := BuildFTtransferCode(sc.CodeScript, targetAddress)
	coinutxos, err := api.FetchFtUTXOList(contractTxid, targetAddress, hex.EncodeToString(ftCodeScript.Bytes()), network)
	if err != nil {
		t.Fatalf("FetchFtUTXOList: %v", err)
	}
	if len(coinutxos) == 0 {
		t.Fatal("目标地址无稳定币 UTXO 可冻结")
	}

	preTXs := make([]*bt.Tx, len(coinutxos))
	prepreTxDatas := make([]string, len(coinutxos))
	for i := range coinutxos {
		preTXs[i], err = api.FetchTXRaw(coinutxos[i].TxID, network)
		if err != nil {
			t.Fatalf("FetchTXRaw(%s): %v", coinutxos[i].TxID, err)
		}
		prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(coinutxos[i].Vout), network)
		if err != nil {
			t.Fatalf("FetchFtPrePreTxData: %v", err)
		}
	}

	adminAddr, _ := bscript.NewAddressFromPublicKey(privKeyAdmin.PubKey(), true)
	feeUTXO, err := api.FetchUTXO(adminAddr.AddressString, 0.01, network)
	if err != nil {
		t.Fatalf("FetchUTXO(fee): %v", err)
	}

	txraw, err := sc.FreezeCoinUTXO(privKeyAdmin, lockTime, coinutxos, feeUTXO, preTXs, prepreTxDatas)
	if err != nil {
		t.Fatalf("FreezeCoinUTXO: %v", err)
	}

	txid, err := api.BroadcastTXRaw(txraw, network)
	if err != nil {
		t.Fatalf("广播 freeze 交易失败: %v", err)
	}
	t.Logf("FreezeCoinUTXO 广播成功 txid=%s lockTime=%d", txid, lockTime)
}

// Ensure unused imports are resolved
var _ = fmt.Sprintf
var _ *bt.Tx
