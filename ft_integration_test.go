// +build integration

// 基于 docs/ft.md 的集成测试
// 运行: go test -tags=integration -v .
// 需设置环境变量: TBC_PRIVATE_KEY (WIF), 可选 FT_CONTRACT_TXID
package contract

import (
	"encoding/hex"
	"math"
	"math/big"
	"os"
	"testing"

	"github.com/libsv/go-bk/bec"
	"github.com/libsv/go-bk/wif"
	"github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

const (
	network        = "testnet"
	addressB       = "1FhSD1YezTXbdRGWzNbNvUj6qeKQ6gZDMq"
	ftName         = "test"
	ftSymbol       = "test"
	ftDecimal      = 6
	ftAmount       = 100000000
	transferAmount = 1000.0
)

func getPrivateKey(t *testing.T) *bec.PrivateKey {
	wifStr := os.Getenv("TBC_PRIVATE_KEY")
	if wifStr == "" {
		t.Skip("TBC_PRIVATE_KEY not set, skipping integration test")
	}
	decoded, err := wif.Decode(wifStr)
	if err != nil {
		t.Fatalf("decode wif: %v", err)
	}
	return decoded.PrivKey
}

// TestIntegration_Mint 对应 ft.md 中的 Mint 流程
func TestIntegration_Mint(t *testing.T) {
	privKey := getPrivateKey(t)

	newToken, err := NewFT(&FtParams{
		Name:    ftName,
		Symbol:  ftSymbol,
		Amount:  ftAmount,
		Decimal: ftDecimal,
	})
	if err != nil {
		t.Fatalf("NewFT: %v", err)
	}

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), false)
	if err != nil {
		t.Fatalf("NewAddressFromPublicKey: %v", err)
	}
	utxo, err := bt.FetchUTXO(addr.AddressString, 0.01, network)
	if err != nil {
		t.Fatalf("FetchUTXO: %v", err)
	}

	txraws, err := newToken.MintFT(privKey, addr.AddressString, utxo)
	if err != nil {
		t.Fatalf("MintFT: %v", err)
	}
	if len(txraws) != 2 {
		t.Fatalf("MintFT returned %d txraws, want 2", len(txraws))
	}
	t.Logf("Mint txSource len=%d, txMint len=%d", len(txraws[0]), len(txraws[1]))
	t.Logf("FT Contract ID: %s", newToken.ContractTxid)
}

// TestIntegration_Transfer 对应 ft.md 中的 Transfer 流程
func TestIntegration_Transfer(t *testing.T) {
	privKey := getPrivateKey(t)
	contractTxid := os.Getenv("FT_CONTRACT_TXID")
	if contractTxid == "" {
		t.Skip("FT_CONTRACT_TXID not set, skipping transfer test")
	}

	token, err := NewFT(contractTxid)
	if err != nil {
		t.Fatalf("NewFT: %v", err)
	}

	info, err := bt.FetchFtInfo(contractTxid, network)
	if err != nil {
		t.Fatalf("FetchFtInfo: %v", err)
	}
	totalSupply, _ := new(big.Int).SetString(info.TotalSupply, 10)
	token.Initialize(&FtInfo{
		Name:        info.Name,
		Symbol:      info.Symbol,
		Decimal:     int(info.Decimal),
		TotalSupply: totalSupply.Int64(),
		CodeScript:  info.CodeScript,
		TapeScript:  info.TapeScript,
	})

	addr, _ := bscript.NewAddressFromPublicKey(privKey.PubKey(), false)
	utxo, err := bt.FetchUTXO(addr.AddressString, 0.01, network)
	if err != nil {
		t.Fatalf("FetchUTXO: %v", err)
	}

	amountBN := big.NewInt(int64(transferAmount * math.Pow10(token.Decimal)))
	codeScript := BuildFTtransferCode(token.CodeScript, addr.AddressString)
	ftutxos, err := bt.FetchFtUTXOs(contractTxid, addr.AddressString, hex.EncodeToString(codeScript.Bytes()), network, amountBN)
	if err != nil {
		t.Fatalf("FetchFtUTXOs: %v", err)
	}

	preTXs := make([]*bt.Tx, len(ftutxos))
	prepreTxDatas := make([]string, len(ftutxos))
	for i := range ftutxos {
		ptx, err := bt.FetchTXRaw(ftutxos[i].TxID, network)
		if err != nil {
			t.Fatalf("FetchTXRaw: %v", err)
		}
		preTXs[i] = ptx
		data, err := bt.FetchFtPrePreTxData(preTXs[i], int(ftutxos[i].Vout), network)
		if err != nil {
			t.Fatalf("FetchFtPrePreTxData: %v", err)
		}
		prepreTxDatas[i] = data
	}

	utxoConv, err := util.FtUTXOToUTXO(ftutxos[0])
	if err != nil {
		t.Fatalf("FtUTXOToUTXO: %v", err)
	}
	// 需要至少一个 TBC utxo 用于 fee
	tbcUtxo, err := bt.FetchUTXO(addr.AddressString, 0.005, network)
	if err != nil {
		t.Fatalf("FetchUTXO for fee: %v", err)
	}

	txraw, err := token.Transfer(privKey, addressB, transferAmount, ftutxos, tbcUtxo, preTXs, prepreTxDatas, 0)
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	t.Logf("Transfer txraw len=%d", len(txraw))
}
