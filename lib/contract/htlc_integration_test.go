//go:build integration
// +build integration

// 链上 HTLC 集成测试（部署 + 广播）。参考 tbc-contract/docs/htlc.md。
//
//	cd tbc-contract-go
//	export RUN_REAL_HTLC_TEST=1
//	export TBC_NETWORK=testnet
//	export TBC_PRIVATE_KEY=<发送方 WIF>
//	export TBC_RECEIVER_ADDRESS=<接收方 P2PKH 地址>
//	go test -tags=integration -v ./lib/contract -run TestHTLC_Integration_DeployBroadcast -count=1
//
// 兼容旧名：RUN_REAL_HTCL_TEST=1 与 RUN_REAL_HTLC_TEST=1 均可
//
// 锁定金额与手续费：默认 HTLC_AMOUNT_TBC=0.001（需 UTXO ≥ 金额 + 手续费）
//
// 测试交易模块 B（对照 docs/htlc.md 与 htlc-chain.ts 中带签名路径）：
//   TestHTLC_Integration_WithdrawBroadcast — 需 RUN_REAL_HTLC_WITHDRAW_TEST=1、HTLC_DEPLOY_TXID、
//   HTLC_SECRET_HEX（32 字节 hex）、TBC_PRIVATE_KEY_B 或 TBC_PRIVKEY_B（接收方，与部署时 TBC_RECEIVER_ADDRESS 一致）。

package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
)

func TestHTLC_Integration_DeployBroadcast(t *testing.T) {
	if os.Getenv("RUN_REAL_HTLC_TEST") != "1" && os.Getenv("RUN_REAL_HTCL_TEST") != "1" {
		t.Skip("set RUN_REAL_HTLC_TEST=1 to run on-chain HTLC deploy test")
	}
	network := strings.TrimSpace(envOr("TBC_NETWORK", "testnet"))
	wifStr := strings.TrimSpace(os.Getenv("TBC_PRIVATE_KEY"))
	if wifStr == "" {
		t.Fatal("TBC_PRIVATE_KEY required")
	}
	receiver := strings.TrimSpace(os.Getenv("TBC_RECEIVER_ADDRESS"))
	if receiver == "" {
		t.Fatal("TBC_RECEIVER_ADDRESS required (P2PKH)")
	}
	decoded, err := wif.DecodeWIF(wifStr)
	if err != nil {
		t.Fatal(err)
	}
	priv := decoded.PrivKey
	senderAddr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	amountStr := envOr("HTLC_AMOUNT_TBC", "0.001")
	amount, err := strconv.ParseFloat(amountStr, 64)
	if err != nil || amount <= 0 {
		t.Fatalf("HTLC_AMOUNT_TBC: %v", err)
	}
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	h := sha256.Sum256(secret)
	hashlock := hex.EncodeToString(h[:])
	timelock := int(time.Now().Unix() + 3600*48)
	utxo, err := api.FetchUTXO(senderAddr.AddressString, amount+0.002, network)
	if err != nil {
		t.Fatalf("FetchUTXO: %v", err)
	}
	raw, err := DeployHTLCWithSign(senderAddr.AddressString, receiver, hashlock, timelock, amount, utxo, priv)
	if err != nil {
		t.Fatalf("DeployHTLCWithSign: %v", err)
	}
	txid, err := api.BroadcastTXRaw(raw, network)
	if err != nil {
		t.Fatalf("BroadcastTXRaw: %v", err)
	}
	t.Logf("HTLC deploy txid=%s (secret preimage hex=%s — 请自行保存用于 withdraw)", txid, hex.EncodeToString(secret))
	_, err = bt.NewTxFromString(raw)
	if err != nil {
		t.Fatal(err)
	}
}

// TestHTLC_Integration_WithdrawBroadcast 对照 docs/htlc.md withdrawWithSign：花费已部署的 HTLC UTXO（接收方 + preimage）。
func TestHTLC_Integration_WithdrawBroadcast(t *testing.T) {
	if os.Getenv("RUN_REAL_HTLC_WITHDRAW_TEST") != "1" {
		t.Skip("set RUN_REAL_HTLC_WITHDRAW_TEST=1 plus HTLC_DEPLOY_TXID, HTLC_SECRET_HEX, TBC_PRIVATE_KEY_B")
	}
	network := strings.TrimSpace(envOr("TBC_NETWORK", "testnet"))
	deployTxid := strings.TrimSpace(os.Getenv("HTLC_DEPLOY_TXID"))
	if deployTxid == "" {
		t.Fatal("HTLC_DEPLOY_TXID required")
	}
	secretHex := strings.TrimSpace(os.Getenv("HTLC_SECRET_HEX"))
	if secretHex == "" {
		t.Fatal("HTLC_SECRET_HEX required (64 hex chars = 32-byte preimage)")
	}
	wifB := strings.TrimSpace(os.Getenv("TBC_PRIVATE_KEY_B"))
	if wifB == "" {
		wifB = strings.TrimSpace(os.Getenv("TBC_PRIVKEY_B"))
	}
	if wifB == "" {
		t.Fatal("TBC_PRIVATE_KEY_B or TBC_PRIVKEY_B required for receiver")
	}
	decB, err := wif.DecodeWIF(wifB)
	if err != nil {
		t.Fatal(err)
	}
	privB := decB.PrivKey
	receiverAddr, err := bscript.NewAddressFromPublicKey(privB.PubKey(), network == "mainnet" || network == "")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := api.FetchTXRaw(deployTxid, network)
	if err != nil {
		t.Fatalf("FetchTXRaw: %v", err)
	}
	if len(tx.Outputs) < 1 {
		t.Fatal("deploy tx has no outputs")
	}
	txidBytes, err := hex.DecodeString(deployTxid)
	if err != nil {
		t.Fatal(err)
	}
	htlcUtxo := &bt.UTXO{
		TxID:          txidBytes,
		Vout:          0,
		LockingScript: tx.Outputs[0].LockingScript,
		Satoshis:      tx.Outputs[0].Satoshis,
	}
	raw, err := WithdrawWithSign(privB, receiverAddr.AddressString, htlcUtxo, secretHex)
	if err != nil {
		t.Fatalf("WithdrawWithSign: %v", err)
	}
	out, err := api.BroadcastTXRaw(raw, network)
	if err != nil {
		t.Fatalf("BroadcastTXRaw: %v", err)
	}
	t.Logf("HTLC withdraw txid=%s", out)
}

func envOr(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v != "" {
		return v
	}
	return def
}
