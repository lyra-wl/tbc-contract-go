// piggy_freeze_run：经 api.FetchUTXO 取 UTXO（对齐 JS fetchUTXO(address, tbcAmount+0.1, network)），再 FreezeTBCWithSign 并可选广播。
// 默认用主网注资地址做 UTXO 查询（与同 WIF 在 mainnet 下的 P2PKH 一致）；勿将私钥写入仓库。
// PIGGY_FETCH_UTXO_ADDRESS 非空时覆盖默认地址；使用默认或主网地址时须 PIGGY_TEST_NETWORK=mainnet，否则会打到 testnet 接口导致无 UTXO。
// PIGGY_TEST_NETWORK 未设置时默认 testnet（若仍用默认主网查询地址，请显式设为 mainnet）。
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/contract"
)

// piggyDefaultFetchUTXOAddress 主网 UTXO 探测地址（不通过 WIF 推导，便于与 testnet 展示地址分离测试）。
const piggyDefaultFetchUTXOAddress = "1P2toD4aKcUsxhTCbUjz5mcd3ajAJB9G1W"

func main() {
	wifStr := strings.TrimSpace(os.Getenv("PIGGY_TEST_WIF"))
	network := strings.TrimSpace(os.Getenv("PIGGY_TEST_NETWORK"))
	if network == "" {
		network = "testnet"
	}
	tbcNumStr := strings.TrimSpace(os.Getenv("PIGGY_TEST_TBC_NUMBER"))
	if tbcNumStr == "" {
		tbcNumStr = "0.001"
	}
	tbcNum, err := strconv.ParseFloat(tbcNumStr, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "PIGGY_TEST_TBC_NUMBER: %v\n", err)
		os.Exit(1)
	}

	extraStr := strings.TrimSpace(os.Getenv("PIGGY_FETCH_UTXO_EXTRA_TBC"))
	if extraStr == "" {
		extraStr = "0.1"
	}
	extraTbc, err := strconv.ParseFloat(extraStr, 64)
	if err != nil || extraTbc < 0 {
		fmt.Fprintf(os.Stderr, "PIGGY_FETCH_UTXO_EXTRA_TBC: %v\n", err)
		os.Exit(1)
	}

	lockStr := strings.TrimSpace(os.Getenv("PIGGY_TEST_LOCK_TIME"))
	var lockTime uint32
	if lockStr == "" {
		headers, err := api.FetchBlockHeaders(network)
		if err != nil || len(headers) == 0 {
			fmt.Fprintf(os.Stderr, "PIGGY_TEST_LOCK_TIME 未设置且 FetchBlockHeaders 失败: %v\n", err)
			os.Exit(1)
		}
		h := headers[0].Height
		if h < 0 || h > 0x7fffffff {
			fmt.Fprintf(os.Stderr, "invalid tip height %d\n", h)
			os.Exit(1)
		}
		lockTime = uint32(h)
	} else {
		lockTime64, err := strconv.ParseUint(lockStr, 10, 32)
		if err != nil {
			fmt.Fprintf(os.Stderr, "PIGGY_TEST_LOCK_TIME: %v\n", err)
			os.Exit(1)
		}
		lockTime = uint32(lockTime64)
	}

	if wifStr == "" {
		fmt.Fprintf(os.Stderr, "need PIGGY_TEST_WIF\n")
		os.Exit(1)
	}

	dec, err := wif.DecodeWIF(wifStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wif: %v\n", err)
		os.Exit(1)
	}
	priv := dec.PrivKey

	fetchAddr := strings.TrimSpace(os.Getenv("PIGGY_FETCH_UTXO_ADDRESS"))
	if fetchAddr == "" {
		fetchAddr = piggyDefaultFetchUTXOAddress
	}

	fetchAmount := tbcNum + extraTbc
	utxo, err := api.FetchUTXO(fetchAddr, fetchAmount, network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FetchUTXO(%s, %g, %s): %v\n", fetchAddr, fetchAmount, network, err)
		os.Exit(1)
	}

	if v := strings.TrimSpace(os.Getenv("FT_FEE_SAT_PER_KB")); v == "" {
		_ = os.Setenv("FT_FEE_SAT_PER_KB", "500")
	}

	raw, err := contract.FreezeTBCWithSign(priv, tbcNum, lockTime, []*bt.UTXO{utxo}, network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FreezeTBCWithSign: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("test_id: piggy_freeze_001 (FetchUTXO-driven)")
	fmt.Printf("FetchUTXO address: %s\n", fetchAddr)
	fmt.Printf("FetchUTXO amount TBC: %g (= tbc_number %g + extra %g)\n", fetchAmount, tbcNum, extraTbc)
	fmt.Println("tx_raw_hex:", raw)

	if strings.TrimSpace(os.Getenv("PIGGY_SKIP_BROADCAST")) == "1" {
		fmt.Println("broadcast: skipped (PIGGY_SKIP_BROADCAST=1)")
		return
	}

	txid, err := api.BroadcastTXRaw(raw, network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "BroadcastTXRaw: %v\n", err)
		os.Exit(2)
	}
	fmt.Println("broadcast_txid:", txid)
}
