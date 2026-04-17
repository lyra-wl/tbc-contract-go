//go:build ignore

// 一次性校验：go run scripts/ms_param_check.go（在 tbc-contract-go 目录）
package main

import (
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/libsv/go-bk/wif"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/contract"
)

func main() {
	const (
		wifStr = "L1u2TmR7hMMMSV9Bx2Lyt3sujbboqEFqnKygnPRnQERhKB4qptuK"
		msAddr = "FGWD7trxrCbLhYZkFj4ajNUvdd9n4nQbg5"
	)
	pks := []string{
		"03f602a928e5a23e992e6f7466d5e47275dddde7fbda2494ffcce9b656330b6654",
		"0227aca3ee4ed9498e35222d1a731fc21e9f8bf91c52ece6228a6066b4db5823e4",
		"0337e5506a33a8c139b2c8bdf0bdb09003fe29af2e75ec8e856aac9b0a159144d9",
	}
	sort.Strings(pks)

	dec, err := wif.DecodeWIF(wifStr)
	if err != nil {
		panic(err)
	}
	mypk := hex.EncodeToString(dec.PrivKey.PubKey().SerialiseCompressed())
	fmt.Println("WIF compressed pubkey:", mypk)

	m, n, err := contract.GetSignatureAndPublicKeyCount(msAddr)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Multisig address M-of-N: %d-of-%d\n", m, n)

	ok, err := contract.VerifyMultiSigAddress(pks, msAddr)
	if err != nil {
		panic(err)
	}
	fmt.Println("VerifyMultiSigAddress(sorted keys, address):", ok)

	calc, err := contract.GetMultiSigAddress(pks, m, n)
	if err != nil {
		panic(err)
	}
	fmt.Println("Recalculated address (sorted keys, M,N):", calc)
	fmt.Println("Equals provided address:", calc == msAddr)
}
