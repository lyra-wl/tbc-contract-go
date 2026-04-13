//go:build ignore
// +build ignore

// 用法：将 JS 打印的 transfer raw hex 存到 stdin 或环境变量 RAW_HEX，与本仓库 tbc-lib-go 的 GetCurrentTxdata 对照。
// go run scripts/compare_current_txdata.go

package main

import (
	"fmt"
	"os"
	"strings"

	bt "github.com/sCrypt-Inc/go-bt/v2"
)

func main() {
	raw := strings.TrimSpace(os.Getenv("RAW_HEX"))
	if raw == "" {
		b, _ := os.ReadFile("/dev/stdin")
		raw = strings.TrimSpace(string(b))
	}
	if raw == "" {
		fmt.Fprintln(os.Stderr, "set RAW_HEX or pipe hex")
		os.Exit(1)
	}
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		panic(err)
	}
	cur, err := bt.GetCurrentTxdata(tx, 0)
	if err != nil {
		panic(err)
	}
	fmt.Println("go_GetCurrentTxdata_len_chars", len(cur))
	fmt.Println(cur)
}
