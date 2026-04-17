// 计算多签地址并（可选）广播创建多签钱包交易，参数写入 JSON 文件。
// 对齐 tbc-contract/docs/multiSIg.md：公钥数组按字典序排列后再算地址与建包。
//
// 仅计算地址 + 写文件（不联网、不广播）：
//
//	go run ./cmd/multisig_wallet_init -dry-run \
//	  -m 2 -n 3 \
//	  -pubkeys '02aaa...,02bbb...,02ccc...' \
//	  -out ./multisig_wallet_params.json
//
// 创建多签钱包并广播（需测试网/主网余额与 TBC_PRIVATE_KEY）：
//
//	export TBC_PRIVATE_KEY=<创建者 WIF>
//	go run ./cmd/multisig_wallet_init \
//	  -network testnet \
//	  -amount 1 \
//	  -m 2 -n 3 \
//	  -pubkeys '02...,02...,02...' \
//	  -out ./multisig_wallet_params.json
//
// 环境变量可代替部分 flag：TBC_NETWORK、MS_PUBKEYS、TBC_PRIVATE_KEY、MS_PARAMS_OUT、MS_CREATE_AMOUNT_TBC。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/libsv/go-bk/wif"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/contract"
)

type walletParams struct {
	Network         string   `json:"network"`
	SignatureCount  int      `json:"signature_count"`
	PublicKeyCount  int      `json:"public_key_count"`
	PublicKeysHex   []string `json:"public_keys_hex"`
	PubkeysSorted   bool     `json:"pubkeys_sorted_lexicographic"`
	MultiSigAddress string   `json:"multisig_address"`
	LockScriptASM   string   `json:"lock_script_asm,omitempty"`
	InitialTBC      float64  `json:"initial_tbc_to_multisig"`
	FunderAddress   string   `json:"funder_p2pkh_address,omitempty"`
	CreateTxRawHex  string   `json:"create_tx_raw_hex,omitempty"`
	CreateTxID      string   `json:"create_txid,omitempty"`
	DryRun          bool     `json:"dry_run"`
	WroteAtUTC      string   `json:"wrote_at_utc"`
	Note            string   `json:"note,omitempty"`
}

func envOr(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v != "" {
		return v
	}
	return def
}

func mainnetAddr(network string) bool {
	return network == "mainnet" || network == ""
}

func parsePubkeys(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() {
	var (
		network  = flag.String("network", "", "testnet / mainnet 或留空读 TBC_NETWORK")
		outPath  = flag.String("out", "", "参数 JSON 路径，默认 MS_PARAMS_OUT 或 ./multisig_wallet_params.json")
		dryRun   = flag.Bool("dry-run", false, "只计算地址并写文件，不拉 UTXO、不广播")
		m        = flag.Int("m", 0, "签名数 M（1–6），可设环境 MS_SIGNATURE_COUNT")
		n        = flag.Int("n", 0, "公钥数 N（3–10），可设环境 MS_PUBLIC_KEY_COUNT")
		amount   = flag.Float64("amount", 0, "创建时打入多签的 TBC，可设 MS_CREATE_AMOUNT_TBC")
		pubCSV   = flag.String("pubkeys", "", "逗号分隔压缩公钥 hex，可设 MS_PUBKEYS")
		wifFlag  = flag.String("wif", "", "创建者 WIF，可设 TBC_PRIVATE_KEY")
	)
	flag.Parse()

	net := strings.TrimSpace(*network)
	if net == "" {
		net = envOr("TBC_NETWORK", "testnet")
	}
	out := strings.TrimSpace(*outPath)
	if out == "" {
		out = envOr("MS_PARAMS_OUT", "multisig_wallet_params.json")
	}

	sig := *m
	if sig == 0 {
		sig = atoiEnv("MS_SIGNATURE_COUNT", 0)
	}
	pkc := *n
	if pkc == 0 {
		pkc = atoiEnv("MS_PUBLIC_KEY_COUNT", 0)
	}
	if sig <= 0 || pkc <= 0 {
		fmt.Fprintln(os.Stderr, "需要 -m/-n 或 MS_SIGNATURE_COUNT / MS_PUBLIC_KEY_COUNT（例如 2/3 多签：-m 2 -n 3）")
		os.Exit(1)
	}

	amt := *amount
	if amt <= 0 {
		amt = atofEnv("MS_CREATE_AMOUNT_TBC", 0)
	}
	if !*dryRun && amt <= 0 {
		fmt.Fprintln(os.Stderr, "非 dry-run 时需要 -amount 或 MS_CREATE_AMOUNT_TBC > 0")
		os.Exit(1)
	}

	pubs := parsePubkeys(*pubCSV)
	if len(pubs) == 0 {
		pubs = parsePubkeys(envOr("MS_PUBKEYS", ""))
	}
	if len(pubs) != pkc {
		fmt.Fprintf(os.Stderr, "公钥个数应为 %d（-n），实际得到 %d 个\n", pkc, len(pubs))
		os.Exit(1)
	}

	sort.Strings(pubs)

	msAddr, err := contract.GetMultiSigAddress(pubs, sig, pkc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "GetMultiSigAddress:", err)
		os.Exit(1)
	}
	asm, err := contract.GetMultiSigLockScript(msAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "GetMultiSigLockScript:", err)
		os.Exit(1)
	}

	params := walletParams{
		Network:         net,
		SignatureCount:  sig,
		PublicKeyCount:  pkc,
		PublicKeysHex:   pubs,
		PubkeysSorted:   true,
		MultiSigAddress: msAddr,
		LockScriptASM:   asm,
		InitialTBC:      amt,
		DryRun:          *dryRun,
		WroteAtUTC:      time.Now().UTC().Format(time.RFC3339),
		Note:            "public_keys_hex 已按字典序排序，与 multiSIg.md 一致；请勿在未排序情况下与链上地址比对",
	}

	if *dryRun {
		if err := writeJSON(out, &params); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("dry-run: wrote", out)
		fmt.Println("multisig_address:", msAddr)
		return
	}

	wifStr := strings.TrimSpace(*wifFlag)
	if wifStr == "" {
		wifStr = strings.TrimSpace(os.Getenv("TBC_PRIVATE_KEY"))
	}
	if wifStr == "" {
		fmt.Fprintln(os.Stderr, "广播创建交易需要 -wif 或 TBC_PRIVATE_KEY")
		os.Exit(1)
	}
	dec, err := wif.DecodeWIF(wifStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "WIF:", err)
		os.Exit(1)
	}
	priv := dec.PrivKey
	from, err := bscript.NewAddressFromPublicKey(priv.PubKey(), mainnetAddr(net))
	if err != nil {
		fmt.Fprintln(os.Stderr, "address:", err)
		os.Exit(1)
	}
	params.FunderAddress = from.AddressString

	utxos, err := api.GetUTXOs(from.AddressString, amt+0.002, net)
	if err != nil {
		fmt.Fprintln(os.Stderr, "GetUTXOs:", err)
		os.Exit(1)
	}

	raw, err := contract.CreateMultiSigWallet(from.AddressString, pubs, sig, pkc, amt, utxos, priv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "CreateMultiSigWallet:", err)
		os.Exit(1)
	}
	params.CreateTxRawHex = raw

	txid, err := api.BroadcastTXRaw(raw, net)
	if err != nil {
		fmt.Fprintln(os.Stderr, "BroadcastTXRaw:", err)
		os.Exit(1)
	}
	params.CreateTxID = txid

	if err := writeJSON(out, &params); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote", out)
	fmt.Println("create_txid:", txid)
	fmt.Println("multisig_address:", msAddr)
}

func writeJSON(path string, v *walletParams) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}

func atoiEnv(key string, def int) int {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	x, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return x
}

func atofEnv(key string, def float64) float64 {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	x, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return x
}
