//go:build integration
// +build integration

// PiggyBank（定时锁）链上集成测试：冻结 TBC → 广播 → 解冻花费 → 广播。
// 对齐 tbc-contract/lib/contract/piggyBank.ts 中 freeze / unfreeze 流程。
//
//	cd tbc-contract-go
//	export RUN_REAL_PIGGYBANK_TEST=1
//	export TBC_PRIVATE_KEY=<WIF>
//	export TBC_NETWORK=testnet
//	# 可选：PIGGY_FREEZE_TBC 冻结金额（默认 0.001）
//	# 可选：PIGGY_FETCH_UTXO_EXTRA_TBC 调用 FetchUTXO 时在冻结额上附加的 TBC（默认 0.1，与 JS fetchUTXO(addr, tbc+0.1, net) 一致）
//	# 可选：PIGGY_LOCK_TIME 锁定高度（uint32）；未设置时使用当前链尖高度，便于一笔流程内即可解冻
//	# 可选：FT_FEE_SAT_PER_KB 测试网易报 66 insufficient priority 时可设 500（与 NFT 集成测试一致）
//	# 可选：PIGGY_FETCH_UTXO_ADDRESS 覆盖默认主网注资地址（默认 1P2to…，与 FetchUTXO/GetPiggyBankCode 一致）；主网跑法须 TBC_NETWORK=mainnet
//	go test -tags=integration -v ./lib/contract -run TestPiggyBank_Integration_FreezeUnfreeze_Broadcast -count=1
//
//	仅测「TBC 定时锁解冻 + 广播」（链上已有一笔冻结交易）：
//	export PIGGY_UNFREEZE_ONLY=1
//	export PIGGY_FREEZE_TXID=<冻结交易 txid>
//	# TBC_PRIVATE_KEY / TBC_NETWORK / PIGGY_FETCH_UTXO_ADDRESS 须与构造冻结时一致
//	go test -tags=integration -v ./lib/contract -run TestPiggyBank_Integration_UnfreezeBroadcast_ExistingFreezeTX -count=1

package contract

import (
	"bytes"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
)

// piggyIntegrationDefaultFetchAddress 主网 UTXO/脚本地址（不依赖 WIF 推导的 testnet 展示地址）。
const piggyIntegrationDefaultFetchAddress = "1P2toD4aKcUsxhTCbUjz5mcd3ajAJB9G1W"

func requirePiggyBankRealRun(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("RUN_REAL_PIGGYBANK_TEST")) != "1" {
		t.Skip("设置 RUN_REAL_PIGGYBANK_TEST=1 以运行 PiggyBank 链上集成测试")
	}
}

func piggyFreezeAmountTBC(t *testing.T) float64 {
	t.Helper()
	s := strings.TrimSpace(os.Getenv("PIGGY_FREEZE_TBC"))
	if s == "" {
		return 0.001
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		t.Fatalf("PIGGY_FREEZE_TBC 须为正数，当前=%q", s)
	}
	return v
}

func piggyFetchUtxoExtraTBC(t *testing.T) float64 {
	t.Helper()
	s := strings.TrimSpace(os.Getenv("PIGGY_FETCH_UTXO_EXTRA_TBC"))
	if s == "" {
		return 0.1
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		t.Fatalf("PIGGY_FETCH_UTXO_EXTRA_TBC 须为非负数，当前=%q", s)
	}
	return v
}

func piggyLockTimeOrTip(t *testing.T, headers []api.BlockHeaderInfo) uint32 {
	t.Helper()
	s := strings.TrimSpace(os.Getenv("PIGGY_LOCK_TIME"))
	if s == "" {
		if len(headers) == 0 {
			t.Fatal("no block headers")
		}
		h := headers[0].Height
		if h < 0 || h > 0x7fffffff {
			t.Fatalf("invalid tip height %d", h)
		}
		return uint32(h)
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		t.Fatalf("PIGGY_LOCK_TIME: %v", err)
	}
	return uint32(v)
}

func findPiggyBankUTXO(t *testing.T, freezeTxid string, freezeTx *bt.Tx, wantScript *bscript.Script) *bt.UTXO {
	t.Helper()
	txidBytes, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(freezeTxid)))
	if err != nil || len(txidBytes) != 32 {
		t.Fatalf("freeze txid: %v", err)
	}
	want := wantScript.Bytes()
	for i, out := range freezeTx.Outputs {
		if out.LockingScript == nil {
			continue
		}
		if bytes.Equal(want, out.LockingScript.Bytes()) {
			return &bt.UTXO{
				TxID:          txidBytes,
				Vout:          uint32(i),
				Satoshis:      out.Satoshis,
				LockingScript: out.LockingScript,
			}
		}
	}
	t.Fatal("冻结交易中未找到与 GetPiggyBankCode 匹配的输出")
	return nil
}

// findPiggyBankUTXOInTx 在冻结交易中扫描输出：能解析为 Piggy 脚本且与 GetPiggyBankCode(addrStr, lt) 一致则命中（无需事先知道 PIGGY_LOCK_TIME）。
func findPiggyBankUTXOInTx(t *testing.T, freezeTxid string, freezeTx *bt.Tx, addrStr string) *bt.UTXO {
	t.Helper()
	txidBytes, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(freezeTxid)))
	if err != nil || len(txidBytes) != 32 {
		t.Fatalf("freeze txid: %v", err)
	}
	for i, out := range freezeTx.Outputs {
		if out.LockingScript == nil {
			continue
		}
		scriptHex := hex.EncodeToString(out.LockingScript.Bytes())
		lt, err := FetchTBCLockTime(scriptHex)
		if err != nil {
			continue
		}
		want, err := GetPiggyBankCode(addrStr, lt)
		if err != nil {
			continue
		}
		if bytes.Equal(want.Bytes(), out.LockingScript.Bytes()) {
			return &bt.UTXO{
				TxID:          txidBytes,
				Vout:          uint32(i),
				Satoshis:      out.Satoshis,
				LockingScript: out.LockingScript,
			}
		}
	}
	t.Fatal("冻结交易中未找到与 addr 匹配的 PiggyBank 输出（检查 PIGGY_FETCH_UTXO_ADDRESS / TBC_NETWORK 是否与冻结时一致）")
	return nil
}

func assertTxOnChain(t *testing.T, network, txid string) {
	t.Helper()
	ok, err := api.IsTxOnChain(txid, network)
	if err != nil {
		t.Fatalf("IsTxOnChain(%s): %v", txid, err)
	}
	if !ok {
		t.Fatalf("交易 %s 未在链上可查", txid)
	}
	t.Logf("链上可查 txid=%s", txid)
}

func broadcastPiggyWithRetry(t *testing.T, network, txraw string) string {
	t.Helper()
	const maxRetry = 5
	var lastErr error
	for attempt := 0; attempt < maxRetry; attempt++ {
		if attempt > 0 {
			t.Logf("广播重试 %d/%d ...", attempt+1, maxRetry)
			time.Sleep(3 * time.Second)
		}
		txid, err := api.BroadcastTXRaw(txraw, network)
		if err == nil {
			return txid
		}
		lastErr = err
		if !strings.Contains(strings.ToLower(err.Error()), "missing inputs") {
			t.Fatalf("BroadcastTXRaw: %v", err)
		}
	}
	t.Fatalf("BroadcastTXRaw（Missing inputs 重试耗尽）: %v", lastErr)
	return ""
}

// TestPiggyBank_Integration_FreezeUnfreeze_Broadcast 先广播冻结交易，再广播解冻交易（花费 piggy 输出）。
func TestPiggyBank_Integration_FreezeUnfreeze_Broadcast(t *testing.T) {
	requirePiggyBankRealRun(t)
	if strings.TrimSpace(os.Getenv("FT_FEE_SAT_PER_KB")) == "" {
		t.Setenv("FT_FEE_SAT_PER_KB", "500")
	}

	network := strings.TrimSpace(envOrDefault("TBC_NETWORK", "testnet"))
	priv := loadPrivKey(t)

	addrStr := piggyIntegrationDefaultFetchAddress
	if v := strings.TrimSpace(os.Getenv("PIGGY_FETCH_UTXO_ADDRESS")); v != "" {
		addrStr = v
	}
	headers, err := api.FetchBlockHeaders(network)
	if err != nil {
		t.Fatalf("FetchBlockHeaders: %v", err)
	}
	lockTime := piggyLockTimeOrTip(t, headers)
	freezeAmt := piggyFreezeAmountTBC(t)
	extraFetch := piggyFetchUtxoExtraTBC(t)

	// 与 tbc-contract 侧 fetchUTXO(address, tbcAmount + 0.1, network) 一致：接口按金额选 UTXO
	utxoFee, err := api.FetchUTXO(addrStr, freezeAmt+extraFetch, network)
	if err != nil {
		t.Fatalf("FetchUTXO: %v", err)
	}

	wantScript, err := GetPiggyBankCode(addrStr, lockTime)
	if err != nil {
		t.Fatal(err)
	}

	rawFreeze, err := FreezeTBCWithSign(priv, freezeAmt, lockTime, []*bt.UTXO{utxoFee}, network)
	if err != nil {
		t.Fatalf("FreezeTBCWithSign: %v", err)
	}

	freezeTxid := broadcastPiggyWithRetry(t, network, rawFreeze)
	t.Logf("冻结广播成功 txid=%s lockTime=%d freezeTBC=%g", freezeTxid, lockTime, freezeAmt)

	waitTxVisible(t, network, freezeTxid, 20, 2*time.Second)
	assertTxOnChain(t, network, freezeTxid)

	freezeTx, err := api.FetchTXRaw(freezeTxid, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(冻结): %v", err)
	}
	piggyUtxo := findPiggyBankUTXO(t, freezeTxid, freezeTx, wantScript)

	scriptHex := hex.EncodeToString(piggyUtxo.LockingScript.Bytes())
	lt, err := FetchTBCLockTime(scriptHex)
	if err != nil {
		t.Fatalf("FetchTBCLockTime: %v", err)
	}
	if lt != lockTime {
		t.Fatalf("链上脚本 lockTime=%d 与构造值 %d 不一致", lt, lockTime)
	}
	t.Logf("Piggy UTXO %s:%d sat=%d", freezeTxid, piggyUtxo.Vout, piggyUtxo.Satoshis)

	rawUnfreeze, err := UnfreezeTBCWithSign(priv, []*bt.UTXO{piggyUtxo}, network)
	if err != nil {
		t.Fatalf("UnfreezeTBCWithSign: %v", err)
	}

	unfreezeTxid := broadcastPiggyWithRetry(t, network, rawUnfreeze)
	t.Logf("解冻广播成功 txid=%s", unfreezeTxid)
	waitTxVisible(t, network, unfreezeTxid, 20, 2*time.Second)
	assertTxOnChain(t, network, unfreezeTxid)
}

// TestPiggyBank_Integration_UnfreezeBroadcast_ExistingFreezeTX 仅测「已有 Piggy 冻结输出 → UnfreezeTBCWithSign → 广播」；用于单独验证 TBC 账户解冻上链。
// 需 RUN_REAL_PIGGYBANK_TEST=1、PIGGY_UNFREEZE_ONLY=1、PIGGY_FREEZE_TXID；地址与网络须与构造该冻结交易时一致。
func TestPiggyBank_Integration_UnfreezeBroadcast_ExistingFreezeTX(t *testing.T) {
	requirePiggyBankRealRun(t)
	if strings.TrimSpace(os.Getenv("PIGGY_UNFREEZE_ONLY")) != "1" {
		t.Skip("设置 PIGGY_UNFREEZE_ONLY=1 且 PIGGY_FREEZE_TXID=<冻结 txid> 以仅跑解冻广播；TBC_PRIVATE_KEY / TBC_NETWORK / PIGGY_FETCH_UTXO_ADDRESS 须与冻结时一致")
	}
	if strings.TrimSpace(os.Getenv("FT_FEE_SAT_PER_KB")) == "" {
		t.Setenv("FT_FEE_SAT_PER_KB", "500")
	}

	freezeTxid := strings.TrimSpace(os.Getenv("PIGGY_FREEZE_TXID"))
	if freezeTxid == "" {
		t.Fatal("PIGGY_UNFREEZE_ONLY=1 时需设置 PIGGY_FREEZE_TXID（冻结交易 txid）")
	}

	network := strings.TrimSpace(envOrDefault("TBC_NETWORK", "testnet"))
	priv := loadPrivKey(t)

	addrStr := piggyIntegrationDefaultFetchAddress
	if v := strings.TrimSpace(os.Getenv("PIGGY_FETCH_UTXO_ADDRESS")); v != "" {
		addrStr = v
	}

	freezeTx, err := api.FetchTXRaw(freezeTxid, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(冻结): %v", err)
	}
	piggyUtxo := findPiggyBankUTXOInTx(t, freezeTxid, freezeTx, addrStr)
	t.Logf("待解冻 Piggy UTXO %s:%d sat=%d", freezeTxid, piggyUtxo.Vout, piggyUtxo.Satoshis)

	rawUnfreeze, err := UnfreezeTBCWithSign(priv, []*bt.UTXO{piggyUtxo}, network)
	if err != nil {
		t.Fatalf("UnfreezeTBCWithSign: %v", err)
	}

	unfreezeTxid := broadcastPiggyWithRetry(t, network, rawUnfreeze)
	t.Logf("解冻广播成功 txid=%s", unfreezeTxid)
	waitTxVisible(t, network, unfreezeTxid, 20, 2*time.Second)
	assertTxOnChain(t, network, unfreezeTxid)
}
