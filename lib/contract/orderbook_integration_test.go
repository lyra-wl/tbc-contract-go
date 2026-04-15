//go:build integration
// +build integration

// OrderBook 模块完整集成测试：创建卖单、撤销卖单、创建买单、撤销买单、撮合交易。
//
// 运行（在 tbc-contract-go 目录下）：
//
//	cd ~/path/to/tbc-contract-go
//	export RUN_REAL_OB_TEST=1
//	export TBC_PRIVATE_KEY=你的WIF
//	export TBC_NETWORK=testnet
//	# 与 orderBook.test.ts / 根目录 test.ts 一致：也可用完整索引根 URL（须能被 api.getBaseURL 识别，建议带尾斜杠）
//	# export TBC_NETWORK=https://api.tbcdev.org/api/tbc/
//	# export TBC_NETWORK=https://testnetlocal.tbcdev.org/api/tbc/
//	export OB_FT_CONTRACT_TXID=FT合约txid
//	export OB_TAX_ADDRESS=税费收取地址
//
//	# ---- 卖单测试 ----
//	export OB_SELL_SALE_VOLUME=10000        # 可选，卖单数量（satoshis），默认 10000
//	export OB_SELL_UNIT_PRICE=100000        # 可选，单价，默认 100000
//	export OB_SELL_FEE_RATE=5000            # 可选，手续费率，默认 5000
//	go test -tags=integration -v ./lib/contract -run TestOrderBook_Integration_MakeSellOrder -count=1
//
//	# ---- 撤销卖单测试（需先成功创建卖单）----
//	export OB_SELL_ORDER_TXID=卖单txid
//	go test -tags=integration -v ./lib/contract -run TestOrderBook_Integration_CancelSellOrder -count=1
//
//	# ---- 买单测试 ----
//	export OB_BUY_SALE_VOLUME=10000         # 可选，买单数量，默认 10000
//	export OB_BUY_UNIT_PRICE=100000         # 可选，单价，默认 100000
//	export OB_BUY_FEE_RATE=5000             # 可选，手续费率，默认 5000
//	go test -tags=integration -v ./lib/contract -run TestOrderBook_Integration_MakeBuyOrder -count=1
//
//	# ---- 撤销买单测试（需先成功创建买单）----
//	export OB_BUY_ORDER_TXID=买单txid
//	go test -tags=integration -v ./lib/contract -run TestOrderBook_Integration_CancelBuyOrder -count=1
//
//	# ---- 撮合交易测试（需同时有卖单和买单）----
//	export OB_SELL_ORDER_TXID=卖单txid
//	export OB_BUY_ORDER_TXID=买单txid
//	export OB_FT_FEE_ADDRESS=FT手续费地址
//	export OB_TBC_FEE_ADDRESS=TBC手续费地址
//	go test -tags=integration -v ./lib/contract -run TestOrderBook_Integration_MatchOrder -count=1
//
//	# ---- 全部测试 ----
//	go test -tags=integration -v ./lib/contract -run TestOrderBook_Integration -count=1
//
//	# ---- 与仓库根目录 test.ts（orderBook.test.ts 参数）一致的一键流程：卖单 → 买单 → 撮合 ----
//	export RUN_REAL_OB_TEST=1
//	export TBC_NETWORK=testnet
//	# JS 侧若设置 TBC_API_BASE 或 http(s) 的 TBC_NETWORK，Go 侧请设同一字符串为 TBC_NETWORK
//	# 可选覆盖 WIF（默认与 test.ts 相同）：OB_SELL_WIF OB_BUY_WIF OB_MATCH_WIF
//	# 可选覆盖数值：OB_JS_UNIT_PRICE OB_JS_SELL_VOLUME OB_JS_BUY_VOLUME OB_JS_FEE_RATE
//	# 可选覆盖合约与地址：OB_FT_CONTRACT_TXID OB_TAX_ADDRESS OB_FT_FEE_ADDRESS OB_TBC_FEE_ADDRESS
//	go test -tags=integration -v ./lib/contract -run TestOrderBook_Integration_FullFlowJSAligned -count=1
package contract

import (
	"encoding/hex"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/libsv/go-bk/bec"
	"github.com/libsv/go-bk/wif"

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

func obEnvUint64(t *testing.T, key string, def uint64) uint64 {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		t.Fatalf("%s 必须是正整数: %v", key, err)
	}
	return v
}

// loadFtCodeScript 从 FT info 获取 codeScript hex
func loadFtCodeScript(t *testing.T, ftContractTxid, network string) string {
	t.Helper()
	info, err := api.FetchFtInfo(ftContractTxid, network)
	if err != nil {
		t.Fatalf("FetchFtInfo: %v", err)
	}
	codeBuf, err := hex.DecodeString(info.CodeScript)
	if err != nil || len(codeBuf) < 1856 {
		t.Fatalf("FT codeScript 无效或过短: len=%d", len(codeBuf))
	}
	t.Logf("FT codeScript length: %d bytes", len(codeBuf))
	return info.CodeScript
}

// 与仓库根目录 test.ts 对齐的默认参数（orderBook.test.ts / JS 成功路径）。
const (
	obJSDefaultFTContract = "a2a7386f8093378a8517e1b808b2101da099a6a0e10c9dfb22fc402d55ee18b8"
	obJSDefaultTaxAddress = "1FdT4hseBZBh6R5XkfLaSpSjuT4Jav9tL6"
	obJSDefaultFeeAddress = "1FdT4hseBZBh6R5XkfLaSpSjuT4Jav9tL6"
	obJSDefaultSellWIF    = "KyyyNMmvYK7oJiqmmfnPgzaJUCEu9oBd6SvGPd9aqgRxTvx7C51i"
	obJSDefaultBuyWIF     = "KwkwVwrgkkizfuEgYLkWq3H8ymY8GVuXC46Tbjehw8ZzRXqFBmAq"
	obJSDefaultMatchWIF   = "L1u2TmR7hMMMSV9Bx2Lyt3sujbboqEFqnKygnPRnQERhKB4qptuK"
)

func obMustDecodeWIF(t *testing.T, s string) *bec.PrivateKey {
	t.Helper()
	decoded, err := wif.DecodeWIF(strings.TrimSpace(s))
	if err != nil {
		t.Fatalf("decode WIF: %v", err)
	}
	return decoded.PrivKey
}

// obWIFEnvOrDefault 优先读环境变量 envKey，否则使用 defaultWIF（与 test.ts 一致时可直跑）。
func obWIFEnvOrDefault(t *testing.T, envKey, defaultWIF string) *bec.PrivateKey {
	t.Helper()
	s := strings.TrimSpace(os.Getenv(envKey))
	if s == "" {
		s = defaultWIF
	}
	return obMustDecodeWIF(t, s)
}

// ===== 1. 创建卖单 =====

func TestOrderBook_Integration_MakeSellOrder(t *testing.T) {
	requireRealOBRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	ftContractTxid := mustEnv(t, "OB_FT_CONTRACT_TXID")
	taxAddress := mustEnv(t, "OB_TAX_ADDRESS")

	ftCodeScript := loadFtCodeScript(t, ftContractTxid, network)

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成地址失败: %v", err)
	}
	t.Logf("地址: %s", addr.AddressString)

	saleVolume := obEnvUint64(t, "OB_SELL_SALE_VOLUME", 10000)
	unitPrice := obEnvUint64(t, "OB_SELL_UNIT_PRICE", 100000)
	feeRate := obEnvUint64(t, "OB_SELL_FEE_RATE", 5000)

	ob := NewOrderBook()

	utxo, err := api.FetchUTXO(addr.AddressString, float64(saleVolume)/1e6+0.02, network)
	if err != nil {
		t.Fatalf("FetchUTXO: %v", err)
	}
	t.Logf("UTXO: txid=%s, vout=%d, satoshis=%d", hex.EncodeToString(utxo.TxID), utxo.Vout, utxo.Satoshis)

	txraw, err := ob.MakeSellOrderWithSign(
		privKey,
		taxAddress,
		saleVolume,
		unitPrice,
		feeRate,
		ftContractTxid,
		ftCodeScript,
		[]*bt.UTXO{utxo},
	)
	if err != nil {
		t.Fatalf("MakeSellOrderWithSign: %v", err)
	}
	t.Logf("卖单 raw 长度: %d bytes", len(txraw)/2)

	txid, err := api.BroadcastTXRaw(txraw, network)
	if err != nil {
		t.Fatalf("广播卖单失败: %v", err)
	}
	t.Logf("✓ 卖单 txid=%s", txid)
	waitTxVisible(t, network, txid, 12, 1*time.Second)
}

// ===== 2. 撤销卖单 =====

func TestOrderBook_Integration_CancelSellOrder(t *testing.T) {
	requireRealOBRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	sellOrderTxid := mustEnv(t, "OB_SELL_ORDER_TXID")

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成地址失败: %v", err)
	}
	t.Logf("地址: %s", addr.AddressString)

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

	sellData, err := GetOrderData(sellUTXO.LockingScript.String(), true)
	if err != nil {
		t.Fatalf("GetOrderData: %v", err)
	}
	t.Logf("卖单数据: holdAddress=%s, saleVolume=%d, unitPrice=%d", sellData.HoldAddress, sellData.SaleVolume, sellData.UnitPrice)

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
		t.Fatalf("广播撤销卖单失败: %v", err)
	}
	t.Logf("✓ 撤销卖单 txid=%s", txid)
	waitTxVisible(t, network, txid, 12, 1*time.Second)
}

// ===== 3. 创建买单 =====

func TestOrderBook_Integration_MakeBuyOrder(t *testing.T) {
	requireRealOBRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	ftContractTxid := mustEnv(t, "OB_FT_CONTRACT_TXID")
	taxAddress := mustEnv(t, "OB_TAX_ADDRESS")

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成地址失败: %v", err)
	}
	t.Logf("地址: %s", addr.AddressString)

	saleVolume := obEnvUint64(t, "OB_BUY_SALE_VOLUME", 10000)
	unitPrice := obEnvUint64(t, "OB_BUY_UNIT_PRICE", 100000)
	feeRate := obEnvUint64(t, "OB_BUY_FEE_RATE", 5000)

	info, err := api.FetchFtInfo(ftContractTxid, network)
	if err != nil {
		t.Fatalf("FetchFtInfo: %v", err)
	}
	t.Logf("FT info: name=%s, symbol=%s, decimal=%d", info.Name, info.Symbol, info.Decimal)

	ftCodeScript := BuildFTtransferCode(info.CodeScript, addr.AddressString)
	ftCodeHex := hex.EncodeToString(ftCodeScript.Bytes())

	precision := uint64(1000000)
	ftAmount := new(big.Int).SetUint64(saleVolume * unitPrice / precision)
	t.Logf("需要 FT 数量: %s", ftAmount.String())

	ftUTXOs, err := api.FetchFtUTXOs(ftContractTxid, addr.AddressString, ftCodeHex, network, ftAmount)
	if err != nil {
		t.Fatalf("FetchFtUTXOs: %v", err)
	}
	t.Logf("获取到 %d 个 FT UTXO", len(ftUTXOs))
	for i, fu := range ftUTXOs {
		t.Logf("  ftUTXO[%d]: txid=%s, vout=%d, ftBalance=%s, satoshis=%d", i, fu.TxID, fu.Vout, fu.FtBalance, fu.Satoshis)
	}

	preTXs := make([]*bt.Tx, 0, len(ftUTXOs))
	prepreTxData := make([]string, 0, len(ftUTXOs))
	for i, fu := range ftUTXOs {
		preTX, err := api.FetchTXRaw(fu.TxID, network)
		if err != nil {
			t.Fatalf("FetchTXRaw(ftUTXO[%d]): %v", i, err)
		}
		preTXs = append(preTXs, preTX)
		ppData, err := api.FetchFtPrePreTxData(preTX, int(fu.Vout), network)
		if err != nil {
			t.Fatalf("FetchFtPrePreTxData(ftUTXO[%d]): %v", i, err)
		}
		prepreTxData = append(prepreTxData, ppData)
	}

	utxo, err := api.FetchUTXO(addr.AddressString, 0.02, network)
	if err != nil {
		t.Fatalf("FetchUTXO: %v", err)
	}

	ob := NewOrderBook()
	txraw, err := ob.MakeBuyOrderWithSign(
		privKey,
		taxAddress,
		saleVolume,
		unitPrice,
		feeRate,
		ftContractTxid,
		[]*bt.UTXO{utxo},
		ftUTXOs,
		preTXs,
		prepreTxData,
	)
	if err != nil {
		t.Fatalf("MakeBuyOrderWithSign: %v", err)
	}
	t.Logf("买单 raw 长度: %d bytes", len(txraw)/2)

	txid, err := api.BroadcastTXRaw(txraw, network)
	if err != nil {
		t.Fatalf("广播买单失败: %v", err)
	}
	t.Logf("✓ 买单 txid=%s", txid)
	waitTxVisible(t, network, txid, 12, 1*time.Second)
}

// ===== 4. 撤销买单 =====

func TestOrderBook_Integration_CancelBuyOrder(t *testing.T) {
	requireRealOBRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	buyOrderTxid := mustEnv(t, "OB_BUY_ORDER_TXID")

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成地址失败: %v", err)
	}
	t.Logf("地址: %s", addr.AddressString)

	buyPreTX, err := api.FetchTXRaw(buyOrderTxid, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(buy): %v", err)
	}
	if len(buyPreTX.Outputs) < 2 {
		t.Fatal("buy tx outputs too few")
	}

	txidBytes, err := hex.DecodeString(buyOrderTxid)
	if err != nil {
		t.Fatalf("decode buy order txid: %v", err)
	}
	buyUTXO := &bt.UTXO{
		TxID:          txidBytes,
		Vout:          0,
		Satoshis:      buyPreTX.Outputs[0].Satoshis,
		LockingScript: buyPreTX.Outputs[0].LockingScript,
	}

	buyData, err := GetOrderData(buyUTXO.LockingScript.String(), true)
	if err != nil {
		t.Fatalf("GetOrderData(buy): %v", err)
	}
	t.Logf("买单数据: holdAddress=%s, saleVolume=%d, unitPrice=%d, ftID=%s", buyData.HoldAddress, buyData.SaleVolume, buyData.UnitPrice, buyData.FtID)

	// FT UTXO 在买单交易的 vout=1（紧随 buy order output）
	ftVout := uint32(1)
	ftUTXO := &bt.UTXO{
		TxID:          txidBytes,
		Vout:          ftVout,
		Satoshis:      buyPreTX.Outputs[ftVout].Satoshis,
		LockingScript: buyPreTX.Outputs[ftVout].LockingScript,
	}

	ftPreTX := buyPreTX
	ftPrePreTxData, err := api.FetchFtPrePreTxData(ftPreTX, int(ftVout), network)
	if err != nil {
		t.Fatalf("FetchFtPrePreTxData: %v", err)
	}

	// 从 tape (vout+1) 中提取 FT 余额
	tapeScript := buyPreTX.Outputs[ftVout+1].LockingScript.Bytes()
	ftBalanceVal := GetBalanceFromTape(hex.EncodeToString(tapeScript))
	if ftBalanceVal == nil {
		ftBalanceVal = big.NewInt(0)
	}
	t.Logf("FT balance from tape: %s", ftBalanceVal.String())

	feeUTXO, err := api.FetchUTXO(addr.AddressString, 0.01, network)
	if err != nil {
		t.Fatalf("FetchUTXO(fee): %v", err)
	}

	ob := NewOrderBook()
	txraw, err := ob.CancelBuyOrderWithSign(
		privKey,
		buyUTXO,
		buyPreTX,
		ftUTXO,
		ftPreTX,
		ftPrePreTxData,
		ftBalanceVal,
		[]*bt.UTXO{feeUTXO},
	)
	if err != nil {
		t.Fatalf("CancelBuyOrderWithSign: %v", err)
	}

	txid, err := api.BroadcastTXRaw(txraw, network)
	if err != nil {
		t.Fatalf("广播撤销买单失败: %v", err)
	}
	t.Logf("✓ 撤销买单 txid=%s", txid)
	waitTxVisible(t, network, txid, 12, 1*time.Second)
}

// ===== 5. 撮合交易 =====

func TestOrderBook_Integration_MatchOrder(t *testing.T) {
	requireRealOBRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	ftContractTxid := mustEnv(t, "OB_FT_CONTRACT_TXID")
	sellOrderTxid := mustEnv(t, "OB_SELL_ORDER_TXID")
	buyOrderTxid := mustEnv(t, "OB_BUY_ORDER_TXID")
	ftFeeAddress := mustEnv(t, "OB_FT_FEE_ADDRESS")
	tbcFeeAddress := mustEnv(t, "OB_TBC_FEE_ADDRESS")

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成地址失败: %v", err)
	}
	t.Logf("撮合者地址: %s", addr.AddressString)

	// 获取买单
	buyPreTX, err := api.FetchTXRaw(buyOrderTxid, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(buy): %v", err)
	}
	buyTxidBytes, _ := hex.DecodeString(buyOrderTxid)
	buyUTXO := &bt.UTXO{
		TxID:          buyTxidBytes,
		Vout:          0,
		Satoshis:      buyPreTX.Outputs[0].Satoshis,
		LockingScript: buyPreTX.Outputs[0].LockingScript,
	}
	buyData, err := GetOrderData(buyUTXO.LockingScript.String(), true)
	if err != nil {
		t.Fatalf("GetOrderData(buy): %v", err)
	}
	t.Logf("买单: saleVolume=%d, unitPrice=%d, feeRate=%d", buyData.SaleVolume, buyData.UnitPrice, buyData.FeeRate)

	// FT UTXO 在买单交易的 vout=1
	ftVout := uint32(1)
	ftUTXO := &bt.UTXO{
		TxID:          buyTxidBytes,
		Vout:          ftVout,
		Satoshis:      buyPreTX.Outputs[ftVout].Satoshis,
		LockingScript: buyPreTX.Outputs[ftVout].LockingScript,
	}
	ftPreTX := buyPreTX

	ftPrePreTxData, err := api.FetchFtPrePreTxData(ftPreTX, int(ftVout), network)
	if err != nil {
		t.Fatalf("FetchFtPrePreTxData: %v", err)
	}

	// 获取卖单
	sellPreTX, err := api.FetchTXRaw(sellOrderTxid, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(sell): %v", err)
	}
	sellTxidBytes, _ := hex.DecodeString(sellOrderTxid)
	sellUTXO := &bt.UTXO{
		TxID:          sellTxidBytes,
		Vout:          0,
		Satoshis:      sellPreTX.Outputs[0].Satoshis,
		LockingScript: sellPreTX.Outputs[0].LockingScript,
	}
	sellData, err := GetOrderData(sellUTXO.LockingScript.String(), true)
	if err != nil {
		t.Fatalf("GetOrderData(sell): %v", err)
	}
	t.Logf("卖单: saleVolume=%d, unitPrice=%d, feeRate=%d", sellData.SaleVolume, sellData.UnitPrice, sellData.FeeRate)

	ftCodeHex := ftUTXO.LockingScript.String()

	tapeScript := ftPreTX.Outputs[ftVout+1].LockingScript.Bytes()
	ftTapeHex := hex.EncodeToString(tapeScript)
	ftBalance := uint64(0)
	if len(tapeScript) >= 51 {
		bal := GetBalanceFromTape(hex.EncodeToString(tapeScript))
		if bal != nil {
			ftBalance = bal.Uint64()
		}
	}
	t.Logf("FT balance from tape: %d", ftBalance)

	// Fee UTXO
	feeUTXO, err := api.FetchUTXO(addr.AddressString, 0.02, network)
	if err != nil {
		t.Fatalf("FetchUTXO(fee): %v", err)
	}

	ob := NewOrderBook()
	txraw, err := ob.MatchOrder(
		privKey,
		buyUTXO, buyPreTX,
		ftUTXO, ftPreTX, ftPrePreTxData,
		sellUTXO, sellPreTX,
		[]*bt.UTXO{feeUTXO},
		ftFeeAddress, tbcFeeAddress,
		ftCodeHex, ftTapeHex,
		ftBalance,
		ftContractTxid,
	)
	if err != nil {
		t.Fatalf("MatchOrder: %v", err)
	}
	t.Logf("撮合交易 raw 长度: %d bytes", len(txraw)/2)

	txid, err := api.BroadcastTXRaw(txraw, network)
	if err != nil {
		t.Fatalf("广播撮合交易失败: %v", err)
	}
	t.Logf("✓ 撮合交易 txid=%s", txid)
	waitTxVisible(t, network, txid, 12, 1*time.Second)
}

// TestOrderBook_Integration_FullFlowJSAligned 与仓库根目录 test.ts 一致：
// sellWIF 挂卖单 → buyWIF 挂买单 → matchWIF 仅作撮合签名；数值默认 unitPrice/sellVolume/buyVolume=1000000，feeRate=1000（6 位精度）。
func TestOrderBook_Integration_FullFlowJSAligned(t *testing.T) {
	requireRealOBRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)

	sellPriv := obWIFEnvOrDefault(t, "OB_SELL_WIF", obJSDefaultSellWIF)
	buyPriv := obWIFEnvOrDefault(t, "OB_BUY_WIF", obJSDefaultBuyWIF)
	matchPriv := obWIFEnvOrDefault(t, "OB_MATCH_WIF", obJSDefaultMatchWIF)

	ftContractTxid := envOrDefault("OB_FT_CONTRACT_TXID", obJSDefaultFTContract)
	taxAddress := envOrDefault("OB_TAX_ADDRESS", obJSDefaultTaxAddress)
	ftFeeAddress := envOrDefault("OB_FT_FEE_ADDRESS", obJSDefaultFeeAddress)
	tbcFeeAddress := envOrDefault("OB_TBC_FEE_ADDRESS", obJSDefaultFeeAddress)

	unitPrice := obEnvUint64(t, "OB_JS_UNIT_PRICE", 1000000)
	sellVolume := obEnvUint64(t, "OB_JS_SELL_VOLUME", 1000000)
	buyVolume := obEnvUint64(t, "OB_JS_BUY_VOLUME", 1000000)
	feeRate := obEnvUint64(t, "OB_JS_FEE_RATE", 1000)

	sellAddr, err := bscript.NewAddressFromPublicKey(sellPriv.PubKey(), true)
	if err != nil {
		t.Fatalf("sell 地址: %v", err)
	}
	buyAddr, err := bscript.NewAddressFromPublicKey(buyPriv.PubKey(), true)
	if err != nil {
		t.Fatalf("buy 地址: %v", err)
	}
	matchAddr, err := bscript.NewAddressFromPublicKey(matchPriv.PubKey(), true)
	if err != nil {
		t.Fatalf("match 地址: %v", err)
	}
	t.Logf("sell=%s buy=%s match=%s", sellAddr.AddressString, buyAddr.AddressString, matchAddr.AddressString)
	t.Logf("ftContract=%s tax=%s feeFT=%s feeTBC=%s", ftContractTxid, taxAddress, ftFeeAddress, tbcFeeAddress)
	t.Logf("unitPrice=%d sellVol=%d buyVol=%d feeRate=%d", unitPrice, sellVolume, buyVolume, feeRate)

	ftInfo, err := api.FetchFtInfo(ftContractTxid, network)
	if err != nil {
		t.Skipf("跳过：无法拉取 FT 信息（请设置 OB_FT_CONTRACT_TXID 为当前测试网 API 可查的合约，与 test.ts 一致可用 a2a7386f…）: %v", err)
	}
	codeBuf, err := hex.DecodeString(ftInfo.CodeScript)
	if err != nil || len(codeBuf) < 1856 {
		t.Fatalf("FT codeScript 无效或过短: len=%d", len(codeBuf))
	}
	ftCodeScript := ftInfo.CodeScript

	var sellTxid string
	{
		ob := NewOrderBook()
		utxo, err := api.FetchUTXO(sellAddr.AddressString, float64(sellVolume)/1e6+0.02, network)
		if err != nil {
			t.Fatalf("FetchUTXO(sell): %v", err)
		}
		txraw, err := ob.MakeSellOrderWithSign(sellPriv, taxAddress, sellVolume, unitPrice, feeRate, ftContractTxid, ftCodeScript, []*bt.UTXO{utxo})
		if err != nil {
			t.Fatalf("MakeSellOrderWithSign: %v", err)
		}
		sellTxid, err = api.BroadcastTXRaw(txraw, network)
		if err != nil {
			t.Fatalf("广播卖单: %v", err)
		}
		t.Logf("✓ 卖单 txid=%s", sellTxid)
		waitTxVisible(t, network, sellTxid, 12, 1*time.Second)
	}

	var buyTxid string
	{
		ob := NewOrderBook()
		ftCodeScriptBuy := BuildFTtransferCode(ftInfo.CodeScript, buyAddr.AddressString)
		ftCodeHex := hex.EncodeToString(ftCodeScriptBuy.Bytes())

		precision := uint64(1000000)
		ftAmount := new(big.Int).SetUint64(buyVolume * unitPrice / precision)

		ftUTXOs, err := api.FetchFtUTXOs(ftContractTxid, buyAddr.AddressString, ftCodeHex, network, ftAmount)
		if err != nil {
			t.Fatalf("FetchFtUTXOs: %v", err)
		}

		preTXs := make([]*bt.Tx, 0, len(ftUTXOs))
		prepreTxData := make([]string, 0, len(ftUTXOs))
		for i, fu := range ftUTXOs {
			preTX, err := api.FetchTXRaw(fu.TxID, network)
			if err != nil {
				t.Fatalf("FetchTXRaw(ft[%d]): %v", i, err)
			}
			preTXs = append(preTXs, preTX)
			pp, err := api.FetchFtPrePreTxData(preTX, int(fu.Vout), network)
			if err != nil {
				t.Fatalf("FetchFtPrePreTxData(ft[%d]): %v", i, err)
			}
			prepreTxData = append(prepreTxData, pp)
		}

		utxo, err := api.FetchUTXO(buyAddr.AddressString, 0.02, network)
		if err != nil {
			t.Fatalf("FetchUTXO(buy): %v", err)
		}

		txraw, err := ob.MakeBuyOrderWithSign(buyPriv, taxAddress, buyVolume, unitPrice, feeRate, ftContractTxid, []*bt.UTXO{utxo}, ftUTXOs, preTXs, prepreTxData)
		if err != nil {
			t.Fatalf("MakeBuyOrderWithSign: %v", err)
		}
		buyTxid, err = api.BroadcastTXRaw(txraw, network)
		if err != nil {
			t.Fatalf("广播买单: %v", err)
		}
		t.Logf("✓ 买单 txid=%s", buyTxid)
		waitTxVisible(t, network, buyTxid, 12, 1*time.Second)
	}

	time.Sleep(1 * time.Second)

	// 撮合：与 TestOrderBook_Integration_MatchOrder 相同构造，私钥为撮合者 matchPriv
	buyPreTX, err := api.FetchTXRaw(buyTxid, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(buy): %v", err)
	}
	buyTxidBytes, _ := hex.DecodeString(buyTxid)
	buyUTXO := &bt.UTXO{
		TxID:          buyTxidBytes,
		Vout:          0,
		Satoshis:      buyPreTX.Outputs[0].Satoshis,
		LockingScript: buyPreTX.Outputs[0].LockingScript,
	}

	ftVout := uint32(1)
	ftUTXO := &bt.UTXO{
		TxID:          buyTxidBytes,
		Vout:          ftVout,
		Satoshis:      buyPreTX.Outputs[ftVout].Satoshis,
		LockingScript: buyPreTX.Outputs[ftVout].LockingScript,
	}
	ftPreTX := buyPreTX

	ftPrePreTxData, err := api.FetchFtPrePreTxData(ftPreTX, int(ftVout), network)
	if err != nil {
		t.Fatalf("FetchFtPrePreTxData: %v", err)
	}

	sellPreTX, err := api.FetchTXRaw(sellTxid, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(sell): %v", err)
	}
	sellTxidBytes, _ := hex.DecodeString(sellTxid)
	sellUTXO := &bt.UTXO{
		TxID:          sellTxidBytes,
		Vout:          0,
		Satoshis:      sellPreTX.Outputs[0].Satoshis,
		LockingScript: sellPreTX.Outputs[0].LockingScript,
	}

	tapeScript := buyPreTX.Outputs[ftVout+1].LockingScript.Bytes()
	ftTapeHex := hex.EncodeToString(tapeScript)
	ftCodeHex := ftUTXO.LockingScript.String()
	ftBalance := uint64(0)
	if len(tapeScript) >= 51 {
		bal := GetBalanceFromTape(hex.EncodeToString(tapeScript))
		if bal != nil {
			ftBalance = bal.Uint64()
		}
	}

	feeUTXO, err := api.FetchUTXO(matchAddr.AddressString, 0.02, network)
	if err != nil {
		t.Fatalf("FetchUTXO(match fee): %v", err)
	}

	ob := NewOrderBook()
	txraw, err := ob.MatchOrder(
		matchPriv,
		buyUTXO, buyPreTX,
		ftUTXO, ftPreTX, ftPrePreTxData,
		sellUTXO, sellPreTX,
		[]*bt.UTXO{feeUTXO},
		ftFeeAddress, tbcFeeAddress,
		ftCodeHex, ftTapeHex,
		ftBalance,
		ftContractTxid,
	)
	if err != nil {
		t.Fatalf("MatchOrder: %v", err)
	}

	matchTxid, err := api.BroadcastTXRaw(txraw, network)
	if err != nil {
		t.Fatalf("广播撮合: %v", err)
	}
	t.Logf("✓ 撮合交易 txid=%s", matchTxid)
	waitTxVisible(t, network, matchTxid, 12, 1*time.Second)
}

// ===== 辅助：无签名构建验证 =====

func TestOrderBook_Integration_BuildSellOrder(t *testing.T) {
	requireRealOBRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	ftContractTxid := mustEnv(t, "OB_FT_CONTRACT_TXID")
	taxAddress := mustEnv(t, "OB_TAX_ADDRESS")

	ftCodeScript := loadFtCodeScript(t, ftContractTxid, network)

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
		taxAddress,
		10000,
		100000,
		5000,
		ftContractTxid,
		ftCodeScript,
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
