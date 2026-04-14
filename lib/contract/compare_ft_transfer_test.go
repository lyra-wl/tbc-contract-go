package contract

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

//go:embed testdata/ft_transfer_001.json
var ftTransfer001JSON []byte

//go:embed testdata/ft_transfer_001_expected.hex
var ftTransfer001ExpectedHex string

// TestCompareFTTransfer_ft_transfer_001 使用 fixtures（与 tbc-contract compare_ft_transfer.js 同源）验证
// Go TransferDecimalString 与 JS FT.transfer 输出同一 transfer raw hex。
func TestCompareFTTransfer_ft_transfer_001(t *testing.T) {
	var root struct {
		Inputs struct {
			PrivateKeyWIF string `json:"private_key_wif"`
			AddressTo     string `json:"address_to"`
			FtAmount      int    `json:"ft_amount"`
			ContractTxid  string `json:"contract_txid"`
			FtInfo        struct {
				Name        string `json:"name"`
				Symbol      string `json:"symbol"`
				Decimal     int    `json:"decimal"`
				TotalSupply int64  `json:"total_supply"`
				CodeScript  string `json:"code_script"`
				TapeScript  string `json:"tape_script"`
			} `json:"ft_info"`
			FtUtxos []struct {
				Txid      string `json:"txid"`
				Vout      uint32 `json:"vout"`
				Satoshis  uint64 `json:"satoshis"`
				Script    string `json:"script"`
				FtBalance int64  `json:"ft_balance"`
			} `json:"ft_utxos"`
			Utxo struct {
				Txid     string `json:"txid"`
				Vout     uint32 `json:"vout"`
				Satoshis uint64 `json:"satoshis"`
				Script   string `json:"script"`
			} `json:"utxo"`
			PreTxHex     string `json:"pre_tx_hex"`
			PrepreTxData string `json:"prepre_tx_data"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(ftTransfer001JSON, &root); err != nil {
		t.Fatalf("parse fixture json: %v", err)
	}
	in := root.Inputs

	w, err := wif.DecodeWIF(in.PrivateKeyWIF)
	if err != nil {
		t.Fatalf("decode WIF: %v", err)
	}
	privKey := w.PrivKey

	ft, err := NewFT(in.ContractTxid)
	if err != nil {
		t.Fatalf("NewFT: %v", err)
	}
	ft.Initialize(&FtInfo{
		Name:        in.FtInfo.Name,
		Symbol:      in.FtInfo.Symbol,
		Decimal:     in.FtInfo.Decimal,
		TotalSupply: in.FtInfo.TotalSupply,
		CodeScript:  in.FtInfo.CodeScript,
		TapeScript:  in.FtInfo.TapeScript,
	})

	ftutxos := make([]*bt.FtUTXO, 0, len(in.FtUtxos))
	for _, u := range in.FtUtxos {
		ftutxos = append(ftutxos, &bt.FtUTXO{
			TxID:      u.Txid,
			Vout:      u.Vout,
			Script:    u.Script,
			Satoshis:  u.Satoshis,
			FtBalance: strconv.FormatInt(u.FtBalance, 10),
		})
	}

	txidBytes, err := hex.DecodeString(in.Utxo.Txid)
	if err != nil || len(txidBytes) != 32 {
		t.Fatalf("fee utxo txid: %v", err)
	}
	lockScript, err := bscript.NewFromHexString(in.Utxo.Script)
	if err != nil {
		t.Fatalf("fee utxo script: %v", err)
	}
	feeUtxo := &bt.UTXO{
		TxID:          txidBytes,
		Vout:          in.Utxo.Vout,
		Satoshis:      in.Utxo.Satoshis,
		LockingScript: lockScript,
	}

	preTX, err := bt.NewTxFromString(strings.TrimSpace(in.PreTxHex))
	if err != nil {
		t.Fatalf("parse pre_tx_hex (NewTxFromString): %v", err)
	}
	preTXs := []*bt.Tx{preTX}
	prepreTxDatas := []string{in.PrepreTxData}

	want := strings.TrimSpace(strings.ToLower(ftTransfer001ExpectedHex))
	amtStr := strconv.Itoa(in.FtAmount)
	got, err := ft.TransferDecimalString(privKey, in.AddressTo, amtStr, ftutxos, feeUtxo, preTXs, prepreTxDatas, 0)
	if err != nil {
		t.Fatalf("TransferDecimalString: %v", err)
	}
	got = strings.TrimSpace(strings.ToLower(got))
	if got != want {
		t.Fatalf("transfer raw mismatch with JS golden: got_len=%d want_len=%d", len(got), len(want))
	}
	t.Logf("ft_transfer_001: Go Transfer raw matches JS golden (%d hex chars), txid ok", len(got))
}
