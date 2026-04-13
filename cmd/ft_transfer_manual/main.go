// 与 tbc-contract/scripts/test.ts 中 Transfer 段同序组装 FT 转账 raw。
// 请在下方 const 块改参数；运行：在 tbc-contract-go 根目录执行
//
//	go run ./cmd/ft_transfer_manual
//
// 若 ftContractTxid 留空，则走 Mint（与 cmd/ft_mint_verify 同源：先播 source 再播 mint）。
// Mint 成功后：把下方 ftContractTxid 填成打印的 mintTxid，或等价地执行
//
//	FT_CONTRACT_TXID=<mintTxid> go run ./cmd/ft_transfer_manual
//
// （环境变量优先于 const，便于一条命令完成转账广播而无需改文件。）
// 广播测试：把 broadcastAfterAssemble 改为 true（会调 TuringBitChain 的 broadcasttx，与 JS API.broadcastTXraw 同类）。
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/libsv/go-bk/wif"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/internal/fttransfertestts"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/contract"
)

// ——— 手动改这里（与 test.ts 顶部常量对齐）———
const (
	network = "testnet"

	// 付款私钥 WIF（对应 test.ts privateKeyA）
	privateKeyWIF = "L1u2TmR7hMMMSV9Bx2Lyt3sujbboqEFqnKygnPRnQERhKB4qptuK"

	// FT 合约 txid（对应 test.ts ftContractTxid）。留空则执行 Mint，成功后打印的 mintTxid 即新合约 id。
	ftContractTxid = "29d1cf29a29e80a55cc096a8a2b2b398447408f4af455469f10d49cd23848ba9"

	// Mint 参数（仅 ftContractTxid 为空时使用；与 ft_mint_verify 默认一致）
	mintName    = "test"
	mintSymbol  = "test"
	mintDecimal = 6
	mintAmount  = int64(100000000)
	// Mint 选 TBC 手续费 UTXO 时的 fetch 额度（与 ft_mint_verify FetchUTXO 0.02 对齐）
	mintFeeFetchTBC = 0.01

	// 收款地址（对应 test.ts addressB）
	transferToAddress = "1JdVc3djVYG7GAMYAd1q9jkpp8gVycTDDq"

	// 转账数量，十进制字符串（对应 test.ts transferTokenAmount = 1000）
	transferAmount = "1000"

	// 对应 test.ts：fetchUTXO(..., tbc_amount+0.01)；仅转 FT 时 tbc_amount=0 → 0.01
	feeFetchTBC = 0.01

	// 若要与某次 JS 组交易共用同一笔手续费 UTXO，填写 prevout；否则留空由接口选币
	lockstepFeeTxid = ""
	lockstepFeeVout = 0

	// false：强制费率与 JS feePerKb(80) 对齐；调试 Go 费率时可改为 true
	respectFeeEnv = true

	// 组装完成后是否调用 HTTP 广播（广播测试）
	broadcastAfterAssemble = true
)

// effectiveFTContractTxid：FT_CONTRACT_TXID 优先（Mint 成功后可直接 export），否则用 const ftContractTxid。
func effectiveFTContractTxid() string {
	if v := strings.TrimSpace(os.Getenv("FT_CONTRACT_TXID")); v != "" {
		return v
	}
	return strings.TrimSpace(ftContractTxid)
}

func waitOnChain(network, txid string, attempts int, interval time.Duration) bool {
	for i := 0; i < attempts; i++ {
		ok, err := api.IsTxOnChain(txid, network)
		if err == nil && ok {
			return true
		}
		time.Sleep(interval)
	}
	return false
}

// broadcastTXRawRetry 在节点报 Missing inputs（常见于手续费 UTXO 刚上链或索引滞后）时间隔重试。
func broadcastTXRawRetry(network, txraw string, maxAttempts int, interval time.Duration, label string) (string, error) {
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		txid, err := api.BroadcastTXRaw(txraw, network)
		if err == nil {
			return txid, nil
		}
		lastErr = err
		if !strings.Contains(strings.ToLower(err.Error()), "missing inputs") {
			return "", err
		}
		time.Sleep(interval)
	}
	return "", fmt.Errorf("%s 广播重试 %d 次后仍失败: %w", label, maxAttempts, lastErr)
}

// runMint 在尚无 FT 合约（ftContractTxid 为空）时铸币；成功后的 mintTxid 可作为后续转账的合约 txid。
func runMint() {
	decoded, err := wif.DecodeWIF(privateKeyWIF)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析私钥 WIF: %v\n", err)
		os.Exit(1)
	}
	privKey := decoded.PrivKey
	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	ft, err := contract.NewFT(&contract.FtParams{
		Name:    mintName,
		Symbol:  mintSymbol,
		Amount:  mintAmount,
		Decimal: mintDecimal,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewFT: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("--- Mint（ftContractTxid 为空）---")
	fmt.Printf("network=%s address=%s\n", network, addr.AddressString)

	utxo, err := api.FetchUTXO(addr.AddressString, mintFeeFetchTBC, network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FetchUTXO: %v\n", err)
		os.Exit(1)
	}

	txraws, err := ft.MintFT(privKey, addr.AddressString, utxo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MintFT: %v\n", err)
		os.Exit(1)
	}

	if !broadcastAfterAssemble {
		fmt.Fprintln(os.Stderr, "未广播（broadcastAfterAssemble=false）。需先播 source 再上链后重建 mint raw。")
		fmt.Println("source_txraw_hex:")
		fmt.Println(txraws[0])
		return
	}

	sourceTxid, err := api.BroadcastTXRaw(txraws[0], network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "广播 source: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("sourceTxid:", sourceTxid)

	if !waitOnChain(network, sourceTxid, 20, time.Second) {
		fmt.Fprintln(os.Stderr, "source 在超时内未上链可见，仍尝试重建 mint…")
	}

	mintRaw, err := ft.RebuildMintTxRawWithBroadcastSource(privKey, addr.AddressString, sourceTxid, txraws[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "RebuildMintTx: %v\n", err)
		os.Exit(1)
	}

	mintTxid, err := broadcastTXRawRetry(network, mintRaw, 8, time.Second, "mint")
	if err != nil {
		fmt.Fprintf(os.Stderr, "广播 mint: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("mintTxid (FT contract id，可填回 ftContractTxid):", mintTxid)
	fmt.Println("mint_raw_hex:")
	fmt.Println(mintRaw)
}

func main() {
	contractID := effectiveFTContractTxid()
	if contractID == "" {
		runMint()
		return
	}

	cfg := fttransfertestts.Config{
		Network:           network,
		PrivateKeyWIF:     privateKeyWIF,
		FTContractTxid:    contractID,
		TransferToAddress: transferToAddress,
		TransferAmount:    transferAmount,
		FeeFetchTBC:       feeFetchTBC,
		LockstepFeeTxid:   lockstepFeeTxid,
		LockstepFeeVout:   lockstepFeeVout,
		RespectFeeEnv:     respectFeeEnv,
		WantStepReport:    false,
	}

	if strings.TrimSpace(os.Getenv("FT_CONTRACT_TXID")) != "" {
		fmt.Println("--- Transfer（FT_CONTRACT_TXID）---")
	} else {
		fmt.Println("--- Transfer（const ftContractTxid）---")
	}
	fmt.Printf("network=%s contractTxid=%s\n", network, contractID)

	rep := fttransfertestts.AssembleWithConfig(cfg)
	if rep.OK != "1" {
		fmt.Fprintf(os.Stderr, "组装失败: %s\n", rep.Error)
		os.Exit(1)
	}

	fmt.Println("txid(标准 wire，与广播/浏览器一致):", rep.TxID)
	fmt.Println("raw_hex:")
	fmt.Println(rep.RawHex) // 单笔 hex 很长时终端会折行，看起来像多行重复前缀，实为同一字符串

	if !broadcastAfterAssemble {
		fmt.Fprintln(os.Stderr, "未广播（broadcastAfterAssemble=false）。JS 侧可用 test.ts 取消注释 API.broadcastTXraw 或自行调广播接口。")
		return
	}

	txid, err := broadcastTXRawRetry(network, rep.RawHex, 8, time.Second, "转账")
	if err != nil {
		fmt.Fprintf(os.Stderr, "广播失败: %v\n", err)
		fmt.Fprintln(os.Stderr, "若持续 Missing inputs：确认手续费 UTXO 仍在、未双花；或等上一笔确认后再试；lockstep 时检查 lockstepFeeTxid/vout。")
		os.Exit(1)
	}
	fmt.Println("广播返回 txid:", txid)
}
