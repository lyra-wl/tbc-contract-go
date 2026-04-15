// 从 PIGGY_TEST_WIF 推导 mainnet / testnet P2PKH 地址并分别查询 api 余额。
// 同一私钥在两网地址不同；若资产在 1xxx 地址须使用 PIGGY_TEST_NETWORK=mainnet（与 FetchUTXO、FreezeTBCWithSign 一致）。
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/libsv/go-bk/wif"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
)

func main() {
	w := strings.TrimSpace(os.Getenv("PIGGY_TEST_WIF"))
	if w == "" {
		fmt.Fprintln(os.Stderr, "export PIGGY_TEST_WIF=<WIF>")
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(w)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wif:", err)
		os.Exit(1)
	}
	pub := dec.PrivKey.PubKey()
	addrMain, err := bscript.NewAddressFromPublicKey(pub, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "address mainnet:", err)
		os.Exit(1)
	}
	addrTest, err := bscript.NewAddressFromPublicKey(pub, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "address testnet:", err)
		os.Exit(1)
	}
	fmt.Println("mainnet_p2pkh:", addrMain.AddressString)
	fmt.Println("testnet_p2pkh:", addrTest.AddressString)

	balMain, err := api.GetTBCBalance(addrMain.AddressString, "mainnet")
	if err != nil {
		fmt.Println("balance_mainnet_error:", err)
	} else {
		fmt.Printf("balance_mainnet_satoshis: %d\n", balMain)
	}
	balTest, err := api.GetTBCBalance(addrTest.AddressString, "testnet")
	if err != nil {
		fmt.Println("balance_testnet_error:", err)
	} else {
		fmt.Printf("balance_testnet_satoshis: %d\n", balTest)
	}
}
