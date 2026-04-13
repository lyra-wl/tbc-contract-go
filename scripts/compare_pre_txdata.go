//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"os"

	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
)

func main() {
	txid := "12a3818c7b7ca02a45388829ffb20c198213d77313f3571a3131e7918d5745da"
	if len(os.Args) > 1 {
		txid = os.Args[1]
	}
	tx, err := api.FetchTXRaw(txid, "testnet")
	if err != nil {
		panic(err)
	}
	p, err := bt.GetPreTxdata(tx, 0)
	if err != nil {
		panic(err)
	}
	fmt.Println(len(p))
	fmt.Print(p)
}
