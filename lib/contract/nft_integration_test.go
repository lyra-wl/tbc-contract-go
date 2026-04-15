//go:build integration
// +build integration

// NFT 链上集成测试（创建合集、铸造、可选转移），与 tbc-contract/docs/nft.md 中 JS 示例流程一致。
//
//	cd tbc-contract-go
//	export RUN_REAL_NFT_TEST=1
//	export TBC_PRIVATE_KEY=<WIF>
//	export TBC_NETWORK=testnet
//	# 可选：NFT_COLLECTION_SUPPLY（默认 3）、NFT_COLLECTION_FEE_TBC（合集+图片手续费 UTXO，默认 0.05）
//	# 可选：NFT_MINT_FEE_TBC（铸造手续费 UTXO，默认 0.002）
//	# 可选：NFT_TRANSFER_FEE_TBC（转移手续费，默认 0.01）
//	# 可选：NFT_FEE_SAT_PER_KB（每 KB 费率 sat；测试网常见 66 insufficient priority，本测试在未设置时默认 500）
//	# 可选：NFT_SKIP_TRANSFER=1 仅测合集+铸造，不广播转移
//	go test -tags=integration -v ./lib/contract -run TestNFT_Integration_CollectionMintTransfer_Broadcast -count=1

package contract

import (
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
)

// 1x1 PNG 占位图（data URI 与 nft.md 中 encodeByBase64 一致思路；体积极小以降低手续费需求）
const nftIntegrationTinyPNGDataURI = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func requireNFTRealRun(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("RUN_REAL_NFT_TEST")) != "1" {
		t.Skip("设置 RUN_REAL_NFT_TEST=1 以运行 NFT 链上集成测试")
	}
}

func nftFeeTBC(key string, def float64) float64 {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return def
	}
	return v
}

func nftCollectionSupply(t *testing.T) int {
	t.Helper()
	s := strings.TrimSpace(os.Getenv("NFT_COLLECTION_SUPPLY"))
	if s == "" {
		return 3
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 || v > 100000 {
		t.Fatalf("NFT_COLLECTION_SUPPLY 须为 [1,100000]，当前=%q", s)
	}
	return v
}

func nftUtxoAPIToBt(u *api.NFTUTXO) (*bt.UTXO, error) {
	txidBytes, err := hex.DecodeString(u.TxID)
	if err != nil || len(txidBytes) != 32 {
		return nil, err
	}
	ls, err := bscript.NewFromHexString(u.Script)
	if err != nil {
		return nil, err
	}
	return &bt.UTXO{
		TxID:          txidBytes,
		Vout:          u.Vout,
		LockingScript: ls,
		Satoshis:      u.Satoshis,
	}, nil
}

func apiNFTInfoToContract(a *api.NFTInfo) *NFTInfo {
	if a == nil {
		return nil
	}
	return &NFTInfo{
		CollectionID:         a.CollectionID,
		CollectionIndex:      a.CollectionIndex,
		CollectionName:       a.CollectionName,
		NftName:              a.NftName,
		NftSymbol:            a.NftSymbol,
		NftAttributes:        a.NftAttributes,
		NftDescription:       a.NftDescription,
		NftTransferTimeCount: a.NftTransferTimeCount,
		NftIcon:              a.NftIcon,
	}
}

func waitNFTMintUtxo(t *testing.T, mintScriptHex, collectionTxid, network string) *api.NFTUTXO {
	t.Helper()
	var lastErr error
	for i := 0; i < 20; i++ {
		u, err := api.FetchNFTUTXO(mintScriptHex, collectionTxid, network)
		if err == nil && u != nil {
			return u
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("等待合集 mint UTXO 失败（已重试）: %v", lastErr)
	return nil
}

// TestNFT_Integration_CollectionMintTransfer_Broadcast 对应 docs/nft.md：createCollection → broadcast → fetchNFTTXO → createNFT → broadcast →（可选）transferNFT → broadcast。
func TestNFT_Integration_CollectionMintTransfer_Broadcast(t *testing.T) {
	requireNFTRealRun(t)
	// 默认 80 sat/KB 在测试网易被拒（66 insufficient priority）；未显式设置 NFT_FEE_SAT_PER_KB 时提高默认。
	if strings.TrimSpace(os.Getenv("NFT_FEE_SAT_PER_KB")) == "" {
		t.Setenv("NFT_FEE_SAT_PER_KB", "500")
	}
	network := strings.TrimSpace(envOrDefault("TBC_NETWORK", "testnet"))
	priv := loadPrivKey(t)

	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	addrStr := addr.AddressString

	supply := nftCollectionSupply(t)
	colFee := nftFeeTBC("NFT_COLLECTION_FEE_TBC", 0.05)
	mintFee := nftFeeTBC("NFT_MINT_FEE_TBC", 0.002)
	transferFee := nftFeeTBC("NFT_TRANSFER_FEE_TBC", 0.01)

	utxoCol, err := api.FetchUTXO(addrStr, colFee, network)
	if err != nil {
		t.Fatalf("FetchUTXO(合集): %v", err)
	}

	cd := &CollectionData{
		Name:        "go-nft-integration",
		Description: "tbc-contract-go integration",
		Supply:      supply,
		File:        nftIntegrationTinyPNGDataURI,
	}
	rawCol, err := CreateCollection(addrStr, priv, cd, []*bt.UTXO{utxoCol})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	collectionTxid, err := api.BroadcastTXRaw(rawCol, network)
	if err != nil {
		t.Fatalf("广播合集: %v", err)
	}
	t.Logf("collection txid=%s supply=%d", collectionTxid, supply)
	waitTxVisible(t, network, collectionTxid, 15, 1*time.Second)

	ms, err := BuildMintScript(addrStr)
	if err != nil {
		t.Fatal(err)
	}
	mintScriptHex := hex.EncodeToString(ms.Bytes())

	nftMintAPI := waitNFTMintUtxo(t, mintScriptHex, collectionTxid, network)
	nftMintBt, err := nftUtxoAPIToBt(nftMintAPI)
	if err != nil {
		t.Fatal(err)
	}

	utxoMintFee, err := api.FetchUTXO(addrStr, mintFee, network)
	if err != nil {
		t.Fatalf("FetchUTXO(铸造手续费): %v", err)
	}

	nftData := &NFTData{
		NftName:     "integration-nft",
		Symbol:      "GNFT",
		Description: "go integration mint",
		Attributes:  "[]",
		File:        "",
	}
	rawNFT, err := CreateNFT(collectionTxid, addrStr, priv, nftData, []*bt.UTXO{utxoMintFee}, nftMintBt)
	if err != nil {
		t.Fatalf("CreateNFT: %v", err)
	}
	contractTxid, err := api.BroadcastTXRaw(rawNFT, network)
	if err != nil {
		t.Fatalf("广播 NFT 铸造: %v", err)
	}
	t.Logf("NFT contract txid=%s", contractTxid)
	waitTxVisible(t, network, contractTxid, 15, 1*time.Second)

	if strings.TrimSpace(os.Getenv("NFT_SKIP_TRANSFER")) == "1" {
		t.Log("已设置 NFT_SKIP_TRANSFER=1，跳过转移广播")
		return
	}

	infoAPI, err := api.FetchNFTInfo(contractTxid, network)
	if err != nil {
		t.Fatalf("FetchNFTInfo: %v", err)
	}
	nft := NewNFT(contractTxid)
	nft.Initialize(apiNFTInfoToContract(infoAPI))

	code, err := BuildCodeScript(infoAPI.CollectionID, uint32(infoAPI.CollectionIndex))
	if err != nil {
		t.Fatal(err)
	}
	codeHex := hex.EncodeToString(code.Bytes())

	var codeUtxo *api.NFTUTXO
	var lastFetchErr error
	for i := 0; i < 20; i++ {
		var u *api.NFTUTXO
		u, lastFetchErr = api.FetchNFTUTXO(codeHex, "", network)
		if lastFetchErr == nil && u != nil {
			codeUtxo = u
			break
		}
		time.Sleep(2 * time.Second)
	}
	if codeUtxo == nil {
		t.Fatalf("取 NFT code UTXO 失败: %v", lastFetchErr)
	}

	preTx, err := api.FetchTXRaw(codeUtxo.TxID, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(pre): %v", err)
	}
	if len(preTx.Inputs) < 1 {
		t.Fatal("preTx 无输入")
	}
	prePrevID := preTx.Inputs[0].PreviousTxIDStr()

	var prePreTx *bt.Tx
	if infoAPI.NftTransferTimeCount == 0 {
		prePreTx, err = api.FetchTXRaw(collectionTxid, network)
	} else {
		prePreTx, err = api.FetchTXRaw(prePrevID, network)
	}
	if err != nil {
		t.Fatalf("FetchTXRaw(pre_pre): %v", err)
	}

	utxoXfer, err := api.FetchUTXO(addrStr, transferFee, network)
	if err != nil {
		t.Fatalf("FetchUTXO(转移手续费): %v", err)
	}

	toStr := strings.TrimSpace(os.Getenv("NFT_TRANSFER_TO"))
	if toStr == "" {
		toStr = addrStr
	}

	rawXfer, err := nft.TransferNFT(addrStr, toStr, priv, []*bt.UTXO{utxoXfer}, preTx, prePreTx, false)
	if err != nil {
		t.Fatalf("TransferNFT: %v", err)
	}
	xferTxid, err := api.BroadcastTXRaw(rawXfer, network)
	if err != nil {
		t.Fatalf("广播转移: %v", err)
	}
	t.Logf("NFT transfer txid=%s -> %s", xferTxid, toStr)
}
