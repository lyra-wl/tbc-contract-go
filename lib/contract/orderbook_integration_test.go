//go:build integration
// +build integration

// 基于 docs/orderBook.md 的挂单/撤单集成测试（含广播）。
//
// 运行：
//   cd ~/path/to/tbc-contract-go
//   export RUN_REAL_OB_TEST=1
//   export TBC_PRIVATE_KEY=你的WIF
//   export TBC_NETWORK=testnet
//   export OB_FT_CONTRACT_TXID=FT合约txid
//   export OB_FT_PARTIAL_HASH=FT合约 code partial hash（64位hex）
//   go test -tags=integration -v ./lib/contract -run TestOrderBook_Integration -count=1
package contract

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
)

func requireRealOBRun(t *testing.T) {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("RUN_REAL_OB_TEST"))
	if raw != "1" {
		t.Skip("默认跳过真实链上挂单测试，设置 RUN_REAL_OB_TEST=1 启用")
	}
}

// TestOrderBook_Integration_MakeSellOrder 测试带私钥的卖单挂单
func TestOrderBook_Integration_MakeSellOrder(t *testing.T) {
	requireRealOBRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	ftContractTxid := mustEnv(t, "OB_FT_CONTRACT_TXID")
	ftPartialHash := mustEnv(t, "OB_FT_PARTIAL_HASH")

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成地址失败: %v", err)
	}

	ob := NewOrderBook()

	saleVolume := uint64(10000)
	unitPrice := uint64(100000)
	feeRate := uint64(5000)

	utxo, err := api.FetchUTXO(addr.AddressString, 0.02, network)
	if err != nil {
		t.Fatalf("FetchUTXO: %v", err)
	}

	txraw, err := ob.MakeSellOrderWithSign(
		privKey,
		saleVolume,
		unitPrice,
		feeRate,
		ftContractTxid,
		ftPartialHash,
		[]*bt.UTXO{utxo},
	)
	if err != nil {
		t.Fatalf("MakeSellOrderWithSign: %v", err)
	}

	txid, err := api.BroadcastTXRaw(txraw, network)
	if err != nil {
		t.Fatalf("广播卖单失败: %v", err)
	}
	t.Logf("卖单 txid=%s", txid)
	waitTxVisible(t, network, txid, 12, 1*time.Second)
}

// TestOrderBook_Integration_CancelSellOrder 测试带私钥的卖单撤销
func TestOrderBook_Integration_CancelSellOrder(t *testing.T) {
	requireRealOBRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	sellOrderTxid := mustEnv(t, "OB_SELL_ORDER_TXID")

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成地址失败: %v", err)
	}

	sellTX, err := api.FetchTXRaw(sellOrderTxid, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(sell): %v", err)
	}
	if len(sellTX.Outputs) == 0 {
		t.Fatal("sell tx has no outputs")
	}

	txidBytes, err := hex.DecodeString(sellOrderTxid)
	if err != nil {
		t.Fatalf("decode sell order txid: %v", err)
	}
	sellUTXO := &bt.UTXO{
		TxID:          txidBytes,
		Vout:          0,
		Satoshis:      sellTX.Outputs[0].Satoshis,
		LockingScript: sellTX.Outputs[0].LockingScript,
	}

	feeUTXO, err := api.FetchUTXO(addr.AddressString, 0.01, network)
	if err != nil {
		t.Fatalf("FetchUTXO(fee): %v", err)
	}

	ob := NewOrderBook()
	txraw, err := ob.CancelSellOrderWithSign(
		privKey,
		sellUTXO,
		[]*bt.UTXO{feeUTXO},
	)
	if err != nil {
		t.Fatalf("CancelSellOrderWithSign: %v", err)
	}

	txid, err := api.BroadcastTXRaw(txraw, network)
	if err != nil {
		t.Fatalf("广播撤单失败: %v", err)
	}
	t.Logf("撤单 txid=%s", txid)
}

// TestOrderBook_Integration_BuildSellOrder 测试无签名卖单构建（不广播，仅验证构建逻辑）
func TestOrderBook_Integration_BuildSellOrder(t *testing.T) {
	requireRealOBRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	ftContractTxid := mustEnv(t, "OB_FT_CONTRACT_TXID")
	ftPartialHash := mustEnv(t, "OB_FT_PARTIAL_HASH")

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成地址失败: %v", err)
	}

	utxo, err := api.FetchUTXO(addr.AddressString, 0.02, network)
	if err != nil {
		t.Fatalf("FetchUTXO: %v", err)
	}

	ob := NewOrderBook()
	txraw, err := ob.BuildSellOrderTX(
		addr.AddressString,
		10000,
		100000,
		5000,
		ftContractTxid,
		ftPartialHash,
		[]*bt.UTXO{utxo},
	)
	if err != nil {
		t.Fatalf("BuildSellOrderTX: %v", err)
	}

	raw, err := hex.DecodeString(txraw)
	if err != nil {
		t.Fatalf("invalid hex: %v", err)
	}
	if len(raw) < 100 {
		t.Error("sell order raw too short")
	}
	t.Logf("BuildSellOrderTX raw length: %d bytes (unsigned)", len(raw))
}
