//go:build integration
// +build integration

// 多签链上集成测试（可广播）。流程对齐 tbc-contract/docs/multiSIg.md。
//
// 在 tbc-contract-go 目录下执行，按需设置下列环境变量之一（每次只跑一类场景）：
//
//	# 公共
//	export TBC_NETWORK=testnet
//
//	# ---- 1) 普通地址 → 多签地址 转 TBC ----
//	export RUN_REAL_MULTISIG_P2PKH_MS_TBC=1
//	export TBC_PRIVATE_KEY=<付款方 WIF，P2PKH>
//	export MS_MULTI_SIG_ADDRESS=<多签地址>
//	export MS_TBC_AMOUNT=0.01
//	go test -tags=integration -v ./lib/contract -run TestMultiSig_Integration_P2PKHToMultiSig_TBC -count=1
//
//	# ---- 2) 多签地址 → 普通地址 转 TBC（需 M-of-N 中 M 个签名者 WIF）----
//	export RUN_REAL_MULTISIG_MS_P2PKH_TBC=1
//	export MS_MULTI_SIG_ADDRESS=<多签地址>
//	export MS_PUBKEYS=<压缩公钥 hex，逗号分隔；测试内会按字典序排序后与 Finish 脚本一致，与 multiSIg.md 创建钱包时一致>
//	export MS_SIGNER_WIFS=<逗号分隔的 M 个 WIF，与 MS_PUBKEYS 中对应公钥匹配>
//	export TBC_RECEIVER_ADDRESS=<收款 P2PKH 地址>
//	export MS_TBC_AMOUNT=0.01
//	go test -tags=integration -v ./lib/contract -run TestMultiSig_Integration_MultiSigToP2PKH_TBC -count=1
//
//	# ---- 3) 普通地址 → 多签地址 转 FT ----
//	export RUN_REAL_MULTISIG_P2PKH_MS_FT=1
//	export TBC_PRIVATE_KEY=<付款方 WIF>
//	export MS_MULTI_SIG_ADDRESS=<多签地址>
//	export MS_FT_CONTRACT_TXID=<FT 合约 txid>
//	export MS_FT_AMOUNT=<人类可读数量，如 100 或 0.5>
//	# 可选：同时向多签打 TBC（与文档 tbc_amount 一致）
//	export MS_FT_WITH_TBC=0
//	go test -tags=integration -v ./lib/contract -run TestMultiSig_Integration_P2PKHToMultiSig_FT -count=1
//
//	# ---- 4) 多签地址 → 普通地址 转 FT（多签 UTXO 建议 vout=0，否则需 merge，见文档）----
//	export RUN_REAL_MULTISIG_MS_P2PKH_FT=1
//	export MS_MULTI_SIG_ADDRESS=<多签地址>
//	export MS_PUBKEYS=<同上>
//	export MS_SIGNER_WIFS=<同上 M 个 WIF>
//	export MS_FT_BUILD_SIGNER_WIF=<可选；默认取 MS_SIGNER_WIFS 第一个。用于 Build 阶段签署 FT 输入>
//	export TBC_RECEIVER_ADDRESS=<收款 P2PKH>
//	export MS_FT_CONTRACT_TXID=<FT 合约 txid>
//	export MS_FT_AMOUNT=<人类可读数量>
//	go test -tags=integration -v ./lib/contract -run TestMultiSig_Integration_MultiSigToP2PKH_FT -count=1

package contract

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/libsv/go-bk/bec"
	"github.com/libsv/go-bk/crypto"
	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

func msMainnetAddress(network string) bool {
	return network == "mainnet" || network == ""
}

func simpleUTXOToBT(u *api.SimpleUTXO) (*bt.UTXO, error) {
	if u == nil {
		return nil, fmt.Errorf("nil utxo")
	}
	txidBytes, err := hex.DecodeString(strings.TrimSpace(u.TxID))
	if err != nil || len(txidBytes) != 32 {
		return nil, fmt.Errorf("invalid txid %q", u.TxID)
	}
	scriptBytes, err := hex.DecodeString(strings.TrimSpace(u.Script))
	if err != nil {
		return nil, fmt.Errorf("script hex: %w", err)
	}
	ls := bscript.NewFromBytes(scriptBytes)
	return &bt.UTXO{
		TxID:          txidBytes,
		Vout:          u.Vout,
		LockingScript: ls,
		Satoshis:      u.Satoshis,
	}, nil
}

// multisigFTRecipientHashHex 与 multiSIg.md 中 hash_from（SHA256 再 HASH160 锁定脚本）一致。
func multisigFTRecipientHashHex(multiSigAddress string) (string, error) {
	asm, err := GetMultiSigLockScript(multiSigAddress)
	if err != nil {
		return "", err
	}
	lockFrom, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(crypto.Hash160(crypto.Sha256(lockFrom.Bytes()))), nil
}

func pickMultisigUMTXOForFT(t *testing.T, scriptASM, network string) *api.SimpleUTXO {
	t.Helper()
	list, err := api.FetchUMTXOs(scriptASM, network)
	if err != nil {
		t.Fatalf("FetchUMTXOs: %v", err)
	}
	for _, u := range list {
		if u != nil && u.Vout == 0 {
			return u
		}
	}
	if len(list) == 0 {
		t.Fatal("no multisig UTXOs for script")
	}
	t.Logf("warning: no UTXO with vout=0; using first entry — 若广播失败请按文档 merge 使多签输出在 vout 0")
	return list[0]
}

func loadFTFromContract(t *testing.T, contractTxid, network string) *FT {
	t.Helper()
	ft, err := NewFT(contractTxid)
	if err != nil {
		t.Fatal(err)
	}
	apiInfo, err := api.FetchFtInfo(contractTxid, network)
	if err != nil {
		t.Fatalf("FetchFtInfo: %v", err)
	}
	var ts int64
	if bi, ok := new(big.Int).SetString(strings.TrimSpace(apiInfo.TotalSupply), 10); ok && bi.IsInt64() {
		ts = bi.Int64()
	}
	ft.Initialize(&FtInfo{
		Name:        apiInfo.Name,
		Symbol:      apiInfo.Symbol,
		Decimal:     int(apiInfo.Decimal),
		TotalSupply: ts,
		CodeScript:  apiInfo.CodeScript,
		TapeScript:  apiInfo.TapeScript,
	})
	return ft
}

func parseCommaList(s string) []string {
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func msDecodeSignerPrivKeys(t *testing.T, csv string) []*bec.PrivateKey {
	t.Helper()
	var keys []*bec.PrivateKey
	for _, w := range parseCommaList(csv) {
		dec, err := wif.DecodeWIF(w)
		if err != nil {
			t.Fatalf("decode WIF: %v", err)
		}
		keys = append(keys, dec.PrivKey)
	}
	return keys
}

func TestMultiSig_Integration_P2PKHToMultiSig_TBC(t *testing.T) {
	if strings.TrimSpace(os.Getenv("RUN_REAL_MULTISIG_P2PKH_MS_TBC")) != "1" {
		t.Skip("set RUN_REAL_MULTISIG_P2PKH_MS_TBC=1 to broadcast P2PKH→multisig TBC")
	}
	network := strings.TrimSpace(envOr("TBC_NETWORK", "testnet"))
	wifStr := strings.TrimSpace(os.Getenv("TBC_PRIVATE_KEY"))
	if wifStr == "" {
		t.Fatal("TBC_PRIVATE_KEY required")
	}
	msAddr := strings.TrimSpace(os.Getenv("MS_MULTI_SIG_ADDRESS"))
	if msAddr == "" {
		t.Fatal("MS_MULTI_SIG_ADDRESS required")
	}
	decoded, err := wif.DecodeWIF(wifStr)
	if err != nil {
		t.Fatal(err)
	}
	priv := decoded.PrivKey
	fromAddr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), msMainnetAddress(network))
	if err != nil {
		t.Fatal(err)
	}
	amtStr := strings.TrimSpace(envOr("MS_TBC_AMOUNT", "0.001"))
	amt, err := strconv.ParseFloat(amtStr, 64)
	if err != nil || amt <= 0 {
		t.Fatalf("MS_TBC_AMOUNT: %v", err)
	}
	utxos, err := api.GetUTXOs(fromAddr.AddressString, amt+0.002, network)
	if err != nil {
		t.Fatalf("GetUTXOs: %v", err)
	}
	raw, err := P2PKHToMultiSigSendTBC(fromAddr.AddressString, msAddr, amt, utxos, priv)
	if err != nil {
		t.Fatalf("P2PKHToMultiSigSendTBC: %v", err)
	}
	txid, err := api.BroadcastTXRaw(raw, network)
	if err != nil {
		t.Fatalf("BroadcastTXRaw: %v", err)
	}
	t.Logf("P2PKH→multisig TBC txid=%s", txid)
}

func TestMultiSig_Integration_MultiSigToP2PKH_TBC(t *testing.T) {
	if strings.TrimSpace(os.Getenv("RUN_REAL_MULTISIG_MS_P2PKH_TBC")) != "1" {
		t.Skip("set RUN_REAL_MULTISIG_MS_P2PKH_TBC=1 to broadcast multisig→P2PKH TBC")
	}
	network := strings.TrimSpace(envOr("TBC_NETWORK", "testnet"))
	msAddr := strings.TrimSpace(os.Getenv("MS_MULTI_SIG_ADDRESS"))
	if msAddr == "" {
		t.Fatal("MS_MULTI_SIG_ADDRESS required")
	}
	pubCSV := strings.TrimSpace(os.Getenv("MS_PUBKEYS"))
	if pubCSV == "" {
		t.Fatal("MS_PUBKEYS required (comma-separated compressed pubkeys hex, same order as wallet)")
	}
	pubKeys := parseCommaList(pubCSV)
	sort.Strings(pubKeys)
	signerWIFs := strings.TrimSpace(os.Getenv("MS_SIGNER_WIFS"))
	if signerWIFs == "" {
		t.Fatal("MS_SIGNER_WIFS required (comma-separated M WIFs)")
	}
	signerPrivs := msDecodeSignerPrivKeys(t, signerWIFs)
	sigN, pkN, err := GetSignatureAndPublicKeyCount(msAddr)
	if err != nil {
		t.Fatal(err)
	}
	if len(signerPrivs) < sigN {
		t.Fatalf("need at least %d signer WIFs (signature count), got %d", sigN, len(signerPrivs))
	}
	signerPrivs = signerPrivs[:sigN]

	toAddr := strings.TrimSpace(os.Getenv("TBC_RECEIVER_ADDRESS"))
	if toAddr == "" {
		t.Fatal("TBC_RECEIVER_ADDRESS required")
	}
	amtStr := strings.TrimSpace(envOr("MS_TBC_AMOUNT", "0.001"))
	amt, err := strconv.ParseFloat(amtStr, 64)
	if err != nil || amt <= 0 {
		t.Fatalf("MS_TBC_AMOUNT: %v", err)
	}
	scriptASM, err := GetMultiSigLockScript(msAddr)
	if err != nil {
		t.Fatal(err)
	}
	umList, err := api.GetUMTXOs(scriptASM, amt+0.002, network)
	if err != nil {
		t.Fatalf("GetUMTXOs: %v", err)
	}
	btUtxos := make([]*bt.UTXO, 0, len(umList))
	for _, u := range umList {
		bu, err := simpleUTXOToBT(u)
		if err != nil {
			t.Fatalf("simpleUTXOToBT: %v", err)
		}
		btUtxos = append(btUtxos, bu)
	}
	partial, err := BuildMultiSigTransactionSendTBC(msAddr, toAddr, amt, btUtxos)
	if err != nil {
		t.Fatalf("BuildMultiSigTransactionSendTBC: %v", err)
	}
	partialTx, err := bt.NewTxFromString(partial.TxRaw)
	if err != nil {
		t.Fatal(err)
	}
	nIn := len(partialTx.Inputs)
	perKeySigs := make([][]string, len(signerPrivs))
	for ki, pk := range signerPrivs {
		sigs, err := SignMultiSigTransactionSendTBC(msAddr, partial, pk)
		if err != nil {
			t.Fatalf("SignMultiSigTransactionSendTBC key %d: %v", ki, err)
		}
		if len(sigs) != nIn {
			t.Fatalf("expected %d sigs per key, got %d", nIn, len(sigs))
		}
		perKeySigs[ki] = sigs
	}
	combined := make([][]string, nIn)
	for i := 0; i < nIn; i++ {
		combined[i] = make([]string, len(signerPrivs))
		for ki := range signerPrivs {
			combined[i][ki] = perKeySigs[ki][i]
		}
	}
	raw, err := FinishMultiSigTransactionSendTBC(partial.TxRaw, combined, pubKeys)
	if err != nil {
		t.Fatalf("FinishMultiSigTransactionSendTBC: %v", err)
	}
	txid, err := api.BroadcastTXRaw(raw, network)
	if err != nil {
		t.Fatalf("BroadcastTXRaw: %v", err)
	}
	t.Logf("multisig→P2PKH TBC txid=%s (pubkeys=%d signers=%d)", txid, pkN, sigN)
}

func TestMultiSig_Integration_P2PKHToMultiSig_FT(t *testing.T) {
	if strings.TrimSpace(os.Getenv("RUN_REAL_MULTISIG_P2PKH_MS_FT")) != "1" {
		t.Skip("set RUN_REAL_MULTISIG_P2PKH_MS_FT=1 to broadcast P2PKH→multisig FT")
	}
	network := strings.TrimSpace(envOr("TBC_NETWORK", "testnet"))
	wifStr := strings.TrimSpace(os.Getenv("TBC_PRIVATE_KEY"))
	if wifStr == "" {
		t.Fatal("TBC_PRIVATE_KEY required")
	}
	msAddr := strings.TrimSpace(os.Getenv("MS_MULTI_SIG_ADDRESS"))
	if msAddr == "" {
		t.Fatal("MS_MULTI_SIG_ADDRESS required")
	}
	contractTxid := strings.TrimSpace(os.Getenv("MS_FT_CONTRACT_TXID"))
	if contractTxid == "" {
		t.Fatal("MS_FT_CONTRACT_TXID required")
	}
	amtHuman := strings.TrimSpace(os.Getenv("MS_FT_AMOUNT"))
	if amtHuman == "" {
		t.Fatal("MS_FT_AMOUNT required (human decimal string)")
	}
	decoded, err := wif.DecodeWIF(wifStr)
	if err != nil {
		t.Fatal(err)
	}
	priv := decoded.PrivKey
	fromAddr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), msMainnetAddress(network))
	if err != nil {
		t.Fatal(err)
	}
	ft := loadFTFromContract(t, contractTxid, network)
	amountBN := util.ParseDecimalToBigInt(amtHuman, ft.Decimal)
	codeHex := hex.EncodeToString(BuildFTtransferCode(ft.CodeScript, fromAddr.AddressString).Bytes())
	ftutxos, err := api.FetchFtUTXOs(contractTxid, fromAddr.AddressString, codeHex, network, amountBN)
	if err != nil {
		t.Fatalf("FetchFtUTXOs: %v", err)
	}
	var tbcSide *float64
	if v := strings.TrimSpace(os.Getenv("MS_FT_WITH_TBC")); v != "" {
		x, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("MS_FT_WITH_TBC: %v", err)
		}
		tbcSide = &x
	}
	feeTbc := 0.01
	if tbcSide != nil {
		feeTbc += *tbcSide
	}
	utxo, err := api.FetchUTXO(fromAddr.AddressString, feeTbc, network)
	if err != nil {
		t.Fatalf("FetchUTXO: %v", err)
	}
	preTXs := make([]*bt.Tx, 0, len(ftutxos))
	prepre := make([]string, 0, len(ftutxos))
	for i, fu := range ftutxos {
		pre, err := api.FetchTXRaw(fu.TxID, network)
		if err != nil {
			t.Fatalf("FetchTXRaw ft[%d]: %v", i, err)
		}
		pp, err := api.FetchFtPrePreTxData(pre, int(fu.Vout), network)
		if err != nil {
			t.Fatalf("FetchFtPrePreTxData ft[%d]: %v", i, err)
		}
		preTXs = append(preTXs, pre)
		prepre = append(prepre, pp)
	}
	raw, err := P2PKHToMultiSigTransferFT(fromAddr.AddressString, msAddr, ft, amtHuman, utxo, ftutxos, preTXs, prepre, priv, tbcSide)
	if err != nil {
		t.Fatalf("P2PKHToMultiSigTransferFT: %v", err)
	}
	txid, err := api.BroadcastTXRaw(raw, network)
	if err != nil {
		t.Fatalf("BroadcastTXRaw: %v", err)
	}
	t.Logf("P2PKH→multisig FT txid=%s", txid)
}

func TestMultiSig_Integration_MultiSigToP2PKH_FT(t *testing.T) {
	if strings.TrimSpace(os.Getenv("RUN_REAL_MULTISIG_MS_P2PKH_FT")) != "1" {
		t.Skip("set RUN_REAL_MULTISIG_MS_P2PKH_FT=1 to broadcast multisig→P2PKH FT")
	}
	network := strings.TrimSpace(envOr("TBC_NETWORK", "testnet"))
	msAddr := strings.TrimSpace(os.Getenv("MS_MULTI_SIG_ADDRESS"))
	if msAddr == "" {
		t.Fatal("MS_MULTI_SIG_ADDRESS required")
	}
	pubCSV := strings.TrimSpace(os.Getenv("MS_PUBKEYS"))
	if pubCSV == "" {
		t.Fatal("MS_PUBKEYS required")
	}
	pubKeys := parseCommaList(pubCSV)
	sort.Strings(pubKeys)
	signerWIFs := strings.TrimSpace(os.Getenv("MS_SIGNER_WIFS"))
	if signerWIFs == "" {
		t.Fatal("MS_SIGNER_WIFS required")
	}
	signerPrivs := msDecodeSignerPrivKeys(t, signerWIFs)
	sigN, _, err := GetSignatureAndPublicKeyCount(msAddr)
	if err != nil {
		t.Fatal(err)
	}
	if len(signerPrivs) < sigN {
		t.Fatalf("need at least %d signer WIFs, got %d", sigN, len(signerPrivs))
	}
	signerPrivs = signerPrivs[:sigN]

	buildWIF := strings.TrimSpace(os.Getenv("MS_FT_BUILD_SIGNER_WIF"))
	var buildPriv *bec.PrivateKey
	if buildWIF != "" {
		d, err := wif.DecodeWIF(buildWIF)
		if err != nil {
			t.Fatal(err)
		}
		buildPriv = d.PrivKey
	} else {
		buildPriv = signerPrivs[0]
	}

	toAddr := strings.TrimSpace(os.Getenv("TBC_RECEIVER_ADDRESS"))
	if toAddr == "" {
		t.Fatal("TBC_RECEIVER_ADDRESS required")
	}
	contractTxid := strings.TrimSpace(os.Getenv("MS_FT_CONTRACT_TXID"))
	if contractTxid == "" {
		t.Fatal("MS_FT_CONTRACT_TXID required")
	}
	amtHuman := strings.TrimSpace(os.Getenv("MS_FT_AMOUNT"))
	if amtHuman == "" {
		t.Fatal("MS_FT_AMOUNT required")
	}

	ft := loadFTFromContract(t, contractTxid, network)
	amountBN := util.ParseDecimalToBigInt(amtHuman, ft.Decimal)

	scriptASM, err := GetMultiSigLockScript(msAddr)
	if err != nil {
		t.Fatal(err)
	}
	hashFrom, err := multisigFTRecipientHashHex(msAddr)
	if err != nil {
		t.Fatal(err)
	}
	codeHex := hex.EncodeToString(BuildFTtransferCode(ft.CodeScript, hashFrom).Bytes())
	ftutxos, err := api.GetFtUTXOsMultiSig(contractTxid, hashFrom, codeHex, network, amountBN)
	if err != nil {
		t.Fatalf("GetFtUTXOsMultiSig: %v", err)
	}

	um := pickMultisigUMTXOForFT(t, scriptASM, network)
	umBT, err := simpleUTXOToBT(um)
	if err != nil {
		t.Fatal(err)
	}
	contractTX, err := api.FetchTXRaw(um.TxID, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(umtxo): %v", err)
	}
	preTXs := make([]*bt.Tx, 0, len(ftutxos))
	prepre := make([]string, 0, len(ftutxos))
	for i, fu := range ftutxos {
		pre, err := api.FetchTXRaw(fu.TxID, network)
		if err != nil {
			t.Fatalf("FetchTXRaw ft[%d]: %v", i, err)
		}
		pp, err := api.FetchFtPrePreTxData(pre, int(fu.Vout), network)
		if err != nil {
			t.Fatalf("FetchFtPrePreTxData ft[%d]: %v", i, err)
		}
		preTXs = append(preTXs, pre)
		prepre = append(prepre, pp)
	}

	partial, err := BuildMultiSigTransactionTransferFT(msAddr, toAddr, ft, amtHuman, umBT, ftutxos, preTXs, prepre, contractTX, buildPriv)
	if err != nil {
		t.Fatalf("BuildMultiSigTransactionTransferFT: %v", err)
	}
	line := make([]string, 0, len(signerPrivs))
	for ki, pk := range signerPrivs {
		sigs, err := SignMultiSigTransactionTransferFT(msAddr, partial, pk)
		if err != nil {
			t.Fatalf("SignMultiSigTransactionTransferFT key %d: %v", ki, err)
		}
		if len(sigs) != 1 {
			t.Fatalf("expected 1 partial sig per signer for FT tx, got %d", len(sigs))
		}
		line = append(line, sigs[0])
	}
	msSigs := [][]string{line}
	raw, err := FinishMultiSigTransactionTransferFT(partial.TxRaw, msSigs, pubKeys)
	if err != nil {
		t.Fatalf("FinishMultiSigTransactionTransferFT: %v", err)
	}
	txid, err := api.BroadcastTXRaw(raw, network)
	if err != nil {
		t.Fatalf("BroadcastTXRaw: %v", err)
	}
	t.Logf("multisig→P2PKH FT txid=%s", txid)
}
