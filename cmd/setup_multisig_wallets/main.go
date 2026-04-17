// 生成 2 个新 P2PKH 钱包（保存 WIF + 压缩公钥 + 地址），用指定出资方 WIF 向二者各转一笔 TBC，
// 再以「2 新公钥 + 出资方公钥」共 3 个公钥创建 2-of-3 多签钱包（与 multiSIg.md / CreateMultiSigWallet 一致）。
//
// 链上协议要求公钥数 N∈[3,10]，因此多签成员为：wallet_a、wallet_b、funder 三者压缩公钥（字典序排序后算地址）。
//
// 用法（在 tbc-contract-go 目录）：
//
//	export GOMODCACHE="$(pwd)/.gomodcache"
//	export TBC_NETWORK=testnet
//	export TBC_FUNDER_WIF='...'   # 可选，默认与测试脚本常用 WIF 相同
//	go run ./cmd/setup_multisig_wallets -out ./multisig_wallets_setup.json
//
// 仅生成密钥并写文件、不广播：-dry-run
//
// 环境变量：FUND_TBC_PER_WALLET（默认 0.05）、MS_CREATE_TBC（默认 0.01）、TBC_NETWORK、OUT_JSON（默认 ./multisig_wallets_setup.json）
// 测试网若广播报 66 insufficient priority，可设 FT_FEE_SAT_PER_KB=2000（与仓库其它集成测试一致）。
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/libsv/go-bk/bec"
	"github.com/libsv/go-bk/chaincfg"
	"github.com/libsv/go-bk/wif"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/contract"
)

const defaultFunderWIF = "L1u2TmR7hMMMSV9Bx2Lyt3sujbboqEFqnKygnPRnQERhKB4qptuK"

type walletRecord struct {
	Label       string `json:"label"`
	WIF         string `json:"wif"`
	PubKeyHex   string `json:"pubkey_hex_compressed"`
	Address     string `json:"address_p2pkh"`
	FundTxID    string `json:"fund_txid,omitempty"`
	FundAmount  string `json:"fund_amount_tbc,omitempty"`
}

type setupDoc struct {
	Network   string `json:"network"`
	CreatedAt string `json:"created_at_utc"`
	DryRun    bool   `json:"dry_run"`
	Note      string `json:"note"`

	FunderAddress string `json:"funder_address"`
	// 不出资方 WIF；请自行保管 TBC_FUNDER_WIF

	WalletA walletRecord `json:"wallet_a"`
	WalletB walletRecord `json:"wallet_b"`

	Multisig struct {
		SignatureCount int      `json:"signature_count"`
		PublicKeyCount int      `json:"public_key_count"`
		PubKeysSorted  []string `json:"public_keys_hex_sorted"`
		Address        string   `json:"multisig_address"`
		CreateTxID     string   `json:"create_txid,omitempty"`
		InitialTBCToMS string   `json:"initial_tbc_to_multisig,omitempty"`
	} `json:"multisig"`
}

func envOr(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v != "" {
		return v
	}
	return def
}

func envFloat(key string, def float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func mainnetP2PKH(network string) bool {
	n := strings.ToLower(strings.TrimSpace(network))
	return n == "" || n == "mainnet"
}

func newWallet(label string, network string) (*bec.PrivateKey, walletRecord, error) {
	priv, err := bec.NewPrivateKey(bec.S256())
	if err != nil {
		return nil, walletRecord{}, err
	}
	netParams := &chaincfg.TestNet
	if mainnetP2PKH(network) {
		netParams = &chaincfg.MainNet
	}
	wifObj, err := wif.NewWIF(priv, netParams, true)
	if err != nil {
		return nil, walletRecord{}, err
	}
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), mainnetP2PKH(network))
	if err != nil {
		return nil, walletRecord{}, err
	}
	rec := walletRecord{
		Label:     label,
		WIF:       wifObj.String(),
		PubKeyHex: hex.EncodeToString(priv.PubKey().SerialiseCompressed()),
		Address:   addr.AddressString,
	}
	return priv, rec, nil
}

func main() {
	dryRun := false
	outPath := envOr("OUT_JSON", "multisig_wallets_setup.json")
	for _, a := range os.Args[1:] {
		if a == "-dry-run" {
			dryRun = true
		}
		if strings.HasPrefix(a, "-out=") {
			outPath = strings.TrimPrefix(a, "-out=")
		}
	}

	network := envOr("TBC_NETWORK", "testnet")
	fundAmt := envFloat("FUND_TBC_PER_WALLET", 0.05)
	msAmt := envFloat("MS_CREATE_TBC", 0.01)

	funderWIF := envOr("TBC_FUNDER_WIF", defaultFunderWIF)
	dec, err := wif.DecodeWIF(funderWIF)
	if err != nil {
		fmt.Fprintf(os.Stderr, "TBC_FUNDER_WIF: %v\n", err)
		os.Exit(1)
	}
	funderPriv := dec.PrivKey
	funderAddr, err := bscript.NewAddressFromPublicKey(funderPriv.PubKey(), mainnetP2PKH(network))
	if err != nil {
		fmt.Fprintf(os.Stderr, "funder address: %v\n", err)
		os.Exit(1)
	}
	funderPKHex := hex.EncodeToString(funderPriv.PubKey().SerialiseCompressed())

	_, recA, err := newWallet("wallet_a", network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wallet_a: %v\n", err)
		os.Exit(1)
	}
	_, recB, err := newWallet("wallet_b", network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wallet_b: %v\n", err)
		os.Exit(1)
	}

	pubKeys := []string{recA.PubKeyHex, recB.PubKeyHex, funderPKHex}
	sort.Strings(pubKeys)

	msAddr, err := contract.GetMultiSigAddress(pubKeys, 2, 3)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetMultiSigAddress: %v\n", err)
		os.Exit(1)
	}

	doc := setupDoc{
		Network:       network,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		DryRun:        dryRun,
		FunderAddress: funderAddr.AddressString,
		WalletA:       recA,
		WalletB:       recB,
		Note:          "含明文私钥，勿提交 git；多签为 2-of-3，公钥为 wallet_a、wallet_b、funder 三者排序后地址。",
	}
	doc.Multisig.SignatureCount = 2
	doc.Multisig.PublicKeyCount = 3
	doc.Multisig.PubKeysSorted = pubKeys
	doc.Multisig.Address = msAddr
	doc.Multisig.InitialTBCToMS = fmt.Sprintf("%g", msAmt)

	if dryRun {
		writeOut(outPath, &doc)
		fmt.Println("dry-run: wrote", outPath)
		fmt.Println("multisig_address:", msAddr)
		return
	}

	// --- 一笔交易同时注资 A、B（避免连续两笔第二笔 priority 不足）---
	utxosFund, err := api.GetUTXOs(funderAddr.AddressString, fundAmt*2+0.03, network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetUTXOs (fund A+B): %v\n", err)
		os.Exit(1)
	}
	rawAB, err := contract.P2PKHToManyP2PKHSendTBC(funderAddr.AddressString, []contract.P2PKHOutputTBC{
		{Address: recA.Address, TBC: fundAmt},
		{Address: recB.Address, TBC: fundAmt},
	}, utxosFund, funderPriv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "P2PKHToManyP2PKHSendTBC: %v\n", err)
		os.Exit(1)
	}
	txidAB, err := api.BroadcastTXRaw(rawAB, network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "broadcast fund A+B: %v\n", err)
		os.Exit(1)
	}
	doc.WalletA.FundTxID = txidAB
	doc.WalletA.FundAmount = fmt.Sprintf("%g", fundAmt)
	doc.WalletB.FundTxID = txidAB
	doc.WalletB.FundAmount = fmt.Sprintf("%g", fundAmt)
	fmt.Println("fund wallet_a+b (same tx) txid:", txidAB)

	time.Sleep(3 * time.Second)

	// --- 创建多签钱包 ---
	utxosMS, err := api.GetUTXOs(funderAddr.AddressString, msAmt+0.02, network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetUTXOs (create multisig): %v\n", err)
		os.Exit(1)
	}
	rawMS, err := contract.CreateMultiSigWallet(funderAddr.AddressString, pubKeys, 2, 3, msAmt, utxosMS, funderPriv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "CreateMultiSigWallet: %v\n", err)
		os.Exit(1)
	}
	txidMS, err := api.BroadcastTXRaw(rawMS, network)
	if err != nil {
		fmt.Fprintf(os.Stderr, "broadcast create multisig: %v\n", err)
		os.Exit(1)
	}
	doc.Multisig.CreateTxID = txidMS
	fmt.Println("create multisig txid:", txidMS)
	fmt.Println("multisig_address:", msAddr)

	writeOut(outPath, &doc)
	fmt.Println("wrote", outPath)
}

func writeOut(path string, doc *setupDoc) {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "json: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
}
