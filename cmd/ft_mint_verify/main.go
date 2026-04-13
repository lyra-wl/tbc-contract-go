// ft_mint_verify：testnet 上 Mint FT，然后用 HTTP GET 调用
// ft/tokenbalance/address/{address}/contract/{contractTxid} 打印原始 JSON，并与 lib/api GetFTBalance 对照。
//
// 用法（在 tbc-contract-go 目录）：
//
//	go run ./cmd/ft_mint_verify
//
// 环境变量（可选）：TBC_NETWORK、TBC_PRIVATE_KEY、FT_MINT_NAME、FT_MINT_SYMBOL、FT_MINT_DECIMAL、FT_MINT_AMOUNT、FT_FEE_SAT_PER_KB（默认 80，与 tbc-contract JS feePerKb(80) 一致）
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/libsv/go-bk/wif"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/contract"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

const (
	defaultWIF    = "L1u2TmR7hMMMSV9Bx2Lyt3sujbboqEFqnKygnPRnQERhKB4qptuK"
	testnetAPIURL = "https://api.tbcdev.org/api/tbc/"
	mainnetAPIURL = "https://api.turingbitchain.io/api/tbc/"
)

func apiBase(network string) string {
	switch network {
	case "testnet":
		return testnetAPIURL
	case "mainnet", "":
		return mainnetAPIURL
	default:
		s := network
		if !strings.HasSuffix(s, "/") {
			s += "/"
		}
		return s
	}
}

func envOr(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envInt64(key string, def int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
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

func broadcastMintRetry(network, txraw string, maxAttempts int, interval time.Duration) (string, error) {
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
	return "", fmt.Errorf("mint 广播重试 %d 次后仍失败: %w", maxAttempts, lastErr)
}

func httpGetBody(url string) (string, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, err
	}
	return string(b), resp.StatusCode, nil
}

func main() {
	network := envOr("TBC_NETWORK", "testnet")
	wifStr := envOr("TBC_PRIVATE_KEY", defaultWIF)

	decoded, err := wif.DecodeWIF(wifStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析 TBC_PRIVATE_KEY: %v\n", err)
		os.Exit(1)
	}
	privKey := decoded.PrivKey
	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	ft, err := contract.NewFT(&contract.FtParams{
		Name:    envOr("FT_MINT_NAME", "test"),
		Symbol:  envOr("FT_MINT_SYMBOL", "test"),
		Amount:  envInt64("FT_MINT_AMOUNT", 100000000),
		Decimal: envInt("FT_MINT_DECIMAL", 6),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewFT: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("--- Mint ---")
	fmt.Printf("network=%s address=%s\n", network, addr.AddressString)

	utxo, err := api.FetchUTXO(addr.AddressString, 0.02, network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FetchUTXO: %v\n", err)
		os.Exit(1)
	}

	txraws, err := ft.MintFT(privKey, addr.AddressString, utxo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MintFT: %v\n", err)
		os.Exit(1)
	}

	sourceTxid, err := api.BroadcastTXRaw(txraws[0], network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "广播 source: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("sourceTxid=%s\n", sourceTxid)

	if !waitOnChain(network, sourceTxid, 20, time.Second) {
		fmt.Fprintf(os.Stderr, "source 在超时内未上链可见，仍尝试重建 mint…\n")
	}

	mintRaw, err := ft.RebuildMintTxRawWithBroadcastSource(privKey, addr.AddressString, sourceTxid, txraws[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "RebuildMintTx: %v\n", err)
		os.Exit(1)
	}

	mintTxid, err := broadcastMintRetry(network, mintRaw, 8, time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "广播 mint: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("mintTxid (FT contract id)=%s\n", mintTxid)

	if !waitOnChain(network, mintTxid, 25, time.Second) {
		fmt.Fprintf(os.Stderr, "mint 在超时内未上链可见\n")
	}

	contractTxid := mintTxid
	base := apiBase(network)
	pkh := addr.PublicKeyHash

	// 你提供的核验路径：按地址查询（服务端会选一个 combine_script，余额可能与索引主键不一致）
	verifyURL := fmt.Sprintf("%sft/tokenbalance/address/%s/contract/%s", base, addr.AddressString, contractTxid)
	fmt.Println("\n--- HTTP GET (address 路径) ---")
	fmt.Println(verifyURL)
	body, status, err := httpGetBody(verifyURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GET: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("HTTP %d\n%s\n", status, body)

	// 与索引键对照：legacy pkh||00 vs 链上脚本 00||pkh
	fmt.Println("\n--- HTTP GET (combinescript 路径对照) ---")
	for _, label := range []struct{ tag, hash string }{
		{"legacy pkh||00", pkh + "00"},
		{"script 00||pkh", "00" + pkh},
	} {
		u := fmt.Sprintf("%sft/tokenbalance/combinescript/%s/contract/%s", base, label.hash, contractTxid)
		b, st, e := httpGetBody(u)
		if e != nil {
			fmt.Printf("%s: GET err %v\n", label.tag, e)
			continue
		}
		fmt.Printf("%s\nHTTP %d\n%s\n\n", u, st, b)
	}

	fmt.Println("\n--- lib/api GetFTBalance（combinescript 双路径） ---")
	balStr, err := api.GetFTBalance(contractTxid, addr.AddressString, network)
	if err != nil {
		fmt.Printf("GetFTBalance error: %v\n", err)
	} else {
		fmt.Printf("balance=%s\n", balStr)
	}
}
