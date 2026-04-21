//go:build integration
// +build integration

// 稳定币链上集成测试，行为见 docs/stableCoin.md。
//
//	RUN_REAL_COIN_TEST=1 TBC_NETWORK=testnet TBC_PRIVATE_KEY=<WIF> \
//	  go test -tags=integration -v ./lib/contract -run TestStableCoin -count=1
//
// 常用可选：COIN_TRANSFER_TO、COIN_CONTRACT_TXID、COIN_ADMIN_ADDRESS、COIN_FUNDING_MIN_TBC、COIN_TRANSFER_FEE_MIN_TBC；
// testnet 未设 NFT_FEE_SAT_PER_KB 时默认 400 sat/KB（避免首铸 66 insufficient priority）。
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

// ensureStablecoinIntegrationRelayFeeForTestnet 在会广播的集成用例中调用：首铸 coinMint 较大，testnet 默认 80 sat/KB 易 66。
// 未设置 NFT_FEE_SAT_PER_KB 时写入 400（可 export 覆盖）。非 testnet 或已设置则不改动。
func ensureStablecoinIntegrationRelayFeeForTestnet(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("NFT_FEE_SAT_PER_KB")) != "" {
		return
	}
	net := strings.TrimSpace(os.Getenv("TBC_NETWORK"))
	if net == "" {
		net = defaultNetwork
	}
	if strings.EqualFold(net, "testnet") {
		t.Setenv("NFT_FEE_SAT_PER_KB", "400")
		t.Logf("testnet 未设置 NFT_FEE_SAT_PER_KB，默认 400 sat/KB（降低 coinMint 广播 66 概率）；可 export 覆盖")
	}
}

func setupStablecoinIntegration(t *testing.T) string {
	t.Helper()
	requireRealCoinRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	ensureStablecoinIntegrationRelayFeeForTestnet(t)
	return network
}

// coinAdminAddress 稳定币管理员侧 P2PKH：与现有用例一致用 mainnet 前缀地址（NewAddressFromPublicKey(..., true)）。
// 若设置 COIN_ADMIN_ADDRESS，则用于 funding / FetchFtUTXOs / transfer code，且必须与私钥推导地址的公钥哈希一致。
func coinAdminAddress(t *testing.T, privKey *bec.PrivateKey) *bscript.Address {
	t.Helper()
	derived, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("从私钥生成地址失败: %v", err)
	}
	raw := strings.TrimSpace(os.Getenv("COIN_ADMIN_ADDRESS"))
	if raw == "" {
		return derived
	}
	custom, err := bscript.NewAddressFromString(raw)
	if err != nil {
		t.Fatalf("COIN_ADMIN_ADDRESS 无效: %v", err)
	}
	if custom.PublicKeyHash != derived.PublicKeyHash {
		t.Fatalf("COIN_ADMIN_ADDRESS=%s 与 TBC_PRIVATE_KEY 推导地址 %s 公钥哈希不一致（无法正确签名链上 FT）",
			custom.AddressString, derived.AddressString)
	}
	return custom
}

// waitFtUtxosIndexed 增发后 FetchFtUTXOs 依赖索引；testnet 可能滞后，短轮询避免偶发空列表。
func waitFtUtxosIndexed(t *testing.T, contractTxid, addr, codeHex, network string, amount *big.Int, maxAttempts int, interval time.Duration) []*bt.FtUTXO {
	t.Helper()
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		list, err := api.FetchFtUTXOs(contractTxid, addr, codeHex, network, amount)
		if err == nil && len(list) > 0 {
			if i > 0 {
				t.Logf("FetchFtUTXOs 就绪 (尝试 %d/%d)", i+1, maxAttempts)
			}
			return list
		}
		lastErr = err
		time.Sleep(interval)
	}
	if lastErr != nil && strings.Contains(strings.ToLower(lastErr.Error()), "zero") {
		t.Skipf("索引未返回 FT 余额（增发已成功）。可稍后重试或检查 TBC_API_BASE。最后错误: %v", lastErr)
	}
	if lastErr != nil {
		t.Fatalf("FetchFtUTXOs 在 %d 次尝试后仍失败: %v", maxAttempts, lastErr)
	}
	t.Fatalf("FetchFtUTXOs 在 %d 次尝试后仍无 UTXO", maxAttempts)
	return nil
}

// requireFundingUTXO 封装 FetchUTXO；失败时打印地址与索引余额，便于区分未注资与单笔 UTXO 小于 min。
func requireFundingUTXO(t *testing.T, label, address, network string, minTBC float64) *bt.UTXO {
	t.Helper()
	utxo, err := api.FetchUTXO(address, minTBC, network)
	if err == nil {
		return utxo
	}
	bal, berr := api.GetTBCBalance(address, network)
	var extra string
	if berr == nil {
		extra = fmt.Sprintf("\n  indexer balance=%d satoshis (~%.6f TBC)", bal, float64(bal)/1e6)
		if bal == 0 {
			extra += "\n  余额为 0：请向管理员地址（未设 COIN_ADMIN_ADDRESS 时为私钥推导地址）转入 testnet TBC 后再跑。"
		} else {
			extra += "\n  索引有余额仍失败：可能尚无单笔 ≥ min 的 UTXO；可调低 COIN_FUNDING_MIN_TBC / COIN_TRANSFER_FEE_MIN_TBC 或在钱包侧合并 UTXO。"
		}
	}
	t.Fatalf("%s: %v\n  address=%s\n  要求单笔 UTXO ≥ %.6f TBC（见环境变量说明）%s", label, err, address, minTBC, extra)
	return nil
}

func loadStableCoinForIntegration(t *testing.T, network, contractTxid string) *StableCoin {
	t.Helper()
	sc, err := NewStableCoin(contractTxid)
	if err != nil {
		t.Fatalf("NewStableCoin: %v", err)
	}
	var name, symbol, codeScript, tapeScript, totalSupplyStr string
	var decimal int
	if coin, err := api.FetchStableCoinInfo(contractTxid, network); err == nil {
		name, symbol = coin.Name, coin.Symbol
		codeScript, tapeScript = coin.CodeScript, coin.TapeScript
		totalSupplyStr = coin.TotalSupply
		decimal = int(coin.Decimal)
	} else {
		loaded := false
		// 索引器 stablecoin/info 的 stablecoinid 多为 coin NFT tx；仅持首铸 mint txid 时由此解析再查。
		if sid, err0 := api.StableCoinIndexerIDFromMintContractTx(contractTxid, network); err0 == nil && sid != "" && sid != contractTxid {
			if coin2, err0b := api.FetchStableCoinInfo(sid, network); err0b == nil {
				name, symbol = coin2.Name, coin2.Symbol
				codeScript, tapeScript = coin2.CodeScript, coin2.TapeScript
				totalSupplyStr = coin2.TotalSupply
				decimal = int(coin2.Decimal)
				loaded = true
			}
		}
		if !loaded {
			info, err2 := api.FetchFtInfo(contractTxid, network)
			if err2 != nil {
				t.Fatalf("FetchStableCoinInfo / FetchFtInfo: %v ; %v", err, err2)
			}
			name, symbol = info.Name, info.Symbol
			codeScript, tapeScript = info.CodeScript, info.TapeScript
			totalSupplyStr = info.TotalSupply
			decimal = int(info.Decimal)
		}
	}
	totalSupply, ok := new(big.Int).SetString(totalSupplyStr, 10)
	if !ok {
		t.Fatalf("非法 TotalSupply: %s", totalSupplyStr)
	}
	sc.Initialize(&FtInfo{
		Name:        name,
		Symbol:      symbol,
		Decimal:     decimal,
		TotalSupply: totalSupply.Int64(),
		CodeScript:  codeScript,
		TapeScript:  tapeScript,
	})
	return sc
}

func newStableCoinFromEnvDefaults(t *testing.T) *StableCoin {
	t.Helper()
	sc, err := NewStableCoin(&FtParams{
		Name:    envOrDefault("COIN_NAME", "USD Test"),
		Symbol:  envOrDefault("COIN_SYMBOL", "USDT"),
		Amount:  parsePositiveInt64(t, "COIN_AMOUNT", 100000000),
		Decimal: parseDecimalRange(t, "COIN_DECIMAL", 6),
	})
	if err != nil {
		t.Fatalf("NewStableCoin: %v", err)
	}
	return sc
}

func fetchFtPreParentsForSpend(t *testing.T, network string, ftutxos []*bt.FtUTXO) ([]*bt.Tx, []string) {
	t.Helper()
	preTXs := make([]*bt.Tx, len(ftutxos))
	prepreTxDatas := make([]string, len(ftutxos))
	for i := range ftutxos {
		tx, err := api.FetchTXRaw(ftutxos[i].TxID, network)
		if err != nil {
			t.Fatalf("FetchTXRaw(%s): %v", ftutxos[i].TxID, err)
		}
		preTXs[i] = tx
		prepreTxDatas[i], err = api.FetchFtPrePreTxData(preTXs[i], int(ftutxos[i].Vout), network)
		if err != nil {
			t.Fatalf("FetchFtPrePreTxData: %v", err)
		}
	}
	return preTXs, prepreTxDatas
}

// TestStableCoin_Integration_CreateCoin 对应 stableCoin.md CreateCoin
func TestStableCoin_Integration_CreateCoin(t *testing.T) {
	network := setupStablecoinIntegration(t)
	privKey := loadPrivKey(t)
	addr := coinAdminAddress(t, privKey)

	sc := newStableCoinFromEnvDefaults(t)

	fundMin := envFloatOrDefault("COIN_FUNDING_MIN_TBC", 0.02)
	utxo := requireFundingUTXO(t, "FetchUTXO", addr.AddressString, network, fundMin)

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

// fetchStableCoinMintParents 对应 stableCoin.md MintCoin：nftPreTX = 当前 coin NFT 交易，nftPrePreTX = 其 input0 的父交易。
func fetchStableCoinMintParents(t *testing.T, network, contractTxid string) (nftPreTX, nftPrePreTX *bt.Tx) {
	t.Helper()
	si, err := api.FetchStableCoinInfo(contractTxid, network)
	if err != nil {
		if sid, err0 := api.StableCoinIndexerIDFromMintContractTx(contractTxid, network); err0 == nil && sid != "" && sid != contractTxid {
			si, err = api.FetchStableCoinInfo(sid, network)
		}
	}
	if err != nil {
		t.Fatalf("FetchStableCoinInfo: %v", err)
	}
	nftPreTX, err = api.FetchTXRaw(si.NftTXID, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(nftPreTX %s): %v", si.NftTXID, err)
	}
	if len(nftPreTX.Inputs) == 0 {
		t.Fatal("nftPreTX 无输入")
	}
	prevID := hex.EncodeToString(nftPreTX.Inputs[0].PreviousTxID())
	nftPrePreTX, err = api.FetchTXRaw(prevID, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(nftPrePreTX %s): %v", prevID, err)
	}
	return nftPreTX, nftPrePreTX
}

// TestStableCoin_Integration_MintCoin 对应 stableCoin.md MintCoin（需已有合约）。
func TestStableCoin_Integration_MintCoin(t *testing.T) {
	network := setupStablecoinIntegration(t)
	privKey := loadPrivKey(t)
	contractTxid := mustEnv(t, "COIN_CONTRACT_TXID")
	mintAmount := envOrDefault("COIN_MINT_AMOUNT", "50000")

	sc := loadStableCoinForIntegration(t, network, contractTxid)
	adminAddr := coinAdminAddress(t, privKey)
	mintTo := mustEnvOrConst(t, "COIN_MINT_TO", adminAddr.AddressString)

	nftPreTX, nftPrePreTX := fetchStableCoinMintParents(t, network, contractTxid)

	fundMin := envFloatOrDefault("COIN_FUNDING_MIN_TBC", 0.02)
	feeUTXO := requireFundingUTXO(t, "FetchUTXO(mint fee)", adminAddr.AddressString, network, fundMin)

	txraw, err := sc.MintCoin(privKey, mintTo, mintAmount, feeUTXO, nftPreTX, nftPrePreTX, "Integration MintCoin")
	if err != nil {
		t.Fatalf("MintCoin: %v", err)
	}
	mintTxid, err := broadcastMintWithRetry(network, txraw, 8, 1*time.Second)
	if err != nil {
		t.Fatalf("广播 MintCoin 失败: %v", err)
	}
	t.Logf("MintCoin 广播成功 txid=%s mintTo=%s amount=%s", mintTxid, mintTo, mintAmount)
}

// TestStableCoin_Integration_IssueMintTransfer 顺序执行：CreateCoin（发行+首铸）→ MintCoin（增发）→ TransferCoin（一次转移），均广播上链。
func TestStableCoin_Integration_IssueMintTransfer(t *testing.T) {
	network := setupStablecoinIntegration(t)
	privKey := loadPrivKey(t)
	adminAddr := coinAdminAddress(t, privKey)
	toAddress := mustEnvOrConst(t, "COIN_TRANSFER_TO", defaultTransferTo)
	mintExtra := envOrDefault("COIN_MINT_EXTRA", "50000")
	transferAmount := envOrDefault("COIN_TRANSFER_AMOUNT", "1000")

	sc := newStableCoinFromEnvDefaults(t)

	fundMin := envFloatOrDefault("COIN_FUNDING_MIN_TBC", 0.02)
	feeTransferMin := envFloatOrDefault("COIN_TRANSFER_FEE_MIN_TBC", 0.01)
	utxo := requireFundingUTXO(t, "FetchUTXO(create)", adminAddr.AddressString, network, fundMin)
	t.Logf("CreateCoin funding UTXO satoshis=%d", utxo.Satoshis)
	utxoTX, err := api.FetchTXRaw(hex.EncodeToString(utxo.TxID), network)
	if err != nil {
		t.Fatalf("FetchTXRaw(utxo): %v", err)
	}

	txraws, err := sc.CreateCoin(privKey, adminAddr.AddressString, utxo, utxoTX, "")
	if err != nil {
		t.Fatalf("CreateCoin: %v", err)
	}
	if len(txraws) != 2 {
		t.Fatalf("CreateCoin 返回交易数=%d，期望=2", len(txraws))
	}

	nftTxid, err := api.BroadcastTXRaw(txraws[0], network)
	if err != nil {
		t.Fatalf("广播 coinNFT: %v", err)
	}
	t.Logf("CoinNFT txid=%s", nftTxid)
	waitTxVisible(t, network, nftTxid, 12, 1*time.Second)

	mintTxid, err := broadcastMintWithRetry(network, txraws[1], 8, 1*time.Second)
	if err != nil {
		t.Fatalf("广播首笔 coinMint: %v", err)
	}
	contractTxid := sc.ContractTxid
	t.Logf("首铸 coinMint txid=%s contractTxid=%s", mintTxid, contractTxid)
	waitTxVisible(t, network, mintTxid, 15, 1*time.Second)

	// MintCoin 的 buildUnlock：preTX=首铸 mint（含 NFT 三输出），prePreTX=首铸 mint 的 input0 父交易（coin NFT），与 fetchStableCoinMintParents 在 NftTXID=首铸时的链式一致。
	nftPreTX, err := bt.NewTxFromString(txraws[1])
	if err != nil {
		t.Fatalf("解析首铸 mint: %v", err)
	}
	nftPrePreTX, err := bt.NewTxFromString(txraws[0])
	if err != nil {
		t.Fatalf("解析 coinNFT: %v", err)
	}

	addBi := ParseDecimalToBigInt(mintExtra, sc.Decimal)
	if !addBi.IsInt64() || addBi.Int64() <= 0 {
		t.Fatalf("COIN_MINT_EXTRA 非法: %s", mintExtra)
	}

	feeMint := requireFundingUTXO(t, "FetchUTXO(mint fee)", adminAddr.AddressString, network, fundMin)
	mintRaw, err := sc.MintCoin(privKey, adminAddr.AddressString, mintExtra, feeMint, nftPreTX, nftPrePreTX, "")
	if err != nil {
		t.Fatalf("MintCoin: %v", err)
	}
	extraMintTxid, err := broadcastMintWithRetry(network, mintRaw, 8, 1*time.Second)
	if err != nil {
		t.Fatalf("广播增发 MintCoin: %v", err)
	}
	t.Logf("增发 MintCoin txid=%s", extraMintTxid)
	waitTxVisible(t, network, extraMintTxid, 15, 1*time.Second)
	sc.TotalSupply += addBi.Int64() // MintCoin 不更新内存 TotalSupply；与链上增发后一致

	amountBN := util.ParseDecimalToBigInt(transferAmount, sc.Decimal)
	ftCodeScript := BuildFTtransferCode(sc.CodeScript, adminAddr.AddressString)
	ftutxos := waitFtUtxosIndexed(t, contractTxid, adminAddr.AddressString, hex.EncodeToString(ftCodeScript.Bytes()), network, amountBN, 35, 2*time.Second)

	preTXs, prepreTxDatas := fetchFtPreParentsForSpend(t, network, ftutxos)

	feeUTXO := requireFundingUTXO(t, "FetchUTXO(transfer fee)", adminAddr.AddressString, network, feeTransferMin)
	transferRaw, err := sc.TransferCoin(privKey, toAddress, transferAmount, ftutxos, feeUTXO, preTXs, prepreTxDatas, 0)
	if err != nil {
		t.Fatalf("TransferCoin: %v", err)
	}
	transferTxid, err := api.BroadcastTXRaw(transferRaw, network)
	if err != nil {
		t.Fatalf("广播 transfer: %v", err)
	}
	t.Logf("Transfer 广播成功 txid=%s -> %s amount=%s", transferTxid, toAddress, transferAmount)
}

// TestStableCoin_Integration_Transfer 对应 stableCoin.md Transfer
func TestStableCoin_Integration_Transfer(t *testing.T) {
	network := setupStablecoinIntegration(t)
	privKey := loadPrivKey(t)
	contractTxid := mustEnv(t, "COIN_CONTRACT_TXID")
	toAddress := mustEnvOrConst(t, "COIN_TRANSFER_TO", defaultTransferTo)
	transferAmount := envOrDefault("COIN_TRANSFER_AMOUNT", "1000")

	sc := loadStableCoinForIntegration(t, network, contractTxid)

	fromAddr := coinAdminAddress(t, privKey)

	amountBN := util.ParseDecimalToBigInt(transferAmount, sc.Decimal)
	ftCodeScript := BuildFTtransferCode(sc.CodeScript, fromAddr.AddressString)
	ftutxos, err := api.FetchFtUTXOs(contractTxid, fromAddr.AddressString, hex.EncodeToString(ftCodeScript.Bytes()), network, amountBN)
	if err != nil {
		t.Fatalf("FetchFtUTXOs: %v", err)
	}
	if len(ftutxos) == 0 {
		t.Fatal("没有可用稳定币 UTXO")
	}

	preTXs, prepreTxDatas := fetchFtPreParentsForSpend(t, network, ftutxos)

	feeTransferMin := envFloatOrDefault("COIN_TRANSFER_FEE_MIN_TBC", 0.01)
	feeUTXO := requireFundingUTXO(t, "FetchUTXO(fee)", fromAddr.AddressString, network, feeTransferMin)

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
	network := setupStablecoinIntegration(t)
	privKeyAdmin := loadPrivKey(t)
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

	preTXs, prepreTxDatas := fetchFtPreParentsForSpend(t, network, coinutxos)

	adminAddr := coinAdminAddress(t, privKeyAdmin)
	feeTransferMin := envFloatOrDefault("COIN_TRANSFER_FEE_MIN_TBC", 0.01)
	feeUTXO := requireFundingUTXO(t, "FetchUTXO(fee)", adminAddr.AddressString, network, feeTransferMin)

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
