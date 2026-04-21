package contract

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

//go:embed testdata/piggybank_freeze_fixture.json
var piggyFreezeFixtureJSON []byte

//go:embed testdata/piggy_freeze_001_expected.hex
var piggyFreeze001ExpectedHex string

// TestPiggyBank_FreezeFixture_piggy_freeze_001 使用 testdata/piggybank_freeze_fixture.json 与 JS piggyBank._freezeTBC
// 对齐后的 golden raw（piggy_freeze_001_expected.hex）；不广播、不访问索引器。
// 更新 golden：先改 fixture，再运行 `npm run piggy-freeze-fixture`，将 piggybank_test.json 中 go.txRawHex 写入 expected.hex。
func TestPiggyBank_FreezeFixture_piggy_freeze_001(t *testing.T) {
	var root struct {
		Disabled bool `json:"disabled"`
		Inputs   struct {
			PrivateKeyWIF string  `json:"private_key_wif"`
			Network       string  `json:"network"`
			TbcNumber     float64 `json:"tbc_number"`
			LockTime      uint32  `json:"lock_time"`
			UTXOs         []struct {
				Txid     string `json:"txid"`
				Vout     uint32 `json:"vout"`
				Satoshis uint64 `json:"satoshis"`
				Script   string `json:"script"`
			} `json:"utxos"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(piggyFreezeFixtureJSON, &root); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if root.Disabled {
		t.Skip("fixture disabled")
	}
	in := root.Inputs
	if len(in.UTXOs) < 1 {
		t.Fatal("fixture.inputs.utxos empty")
	}
	u0 := in.UTXOs[0]
	w, err := wif.DecodeWIF(strings.TrimSpace(in.PrivateKeyWIF))
	if err != nil {
		t.Fatalf("wif: %v", err)
	}
	tid, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(u0.Txid)))
	if err != nil || len(tid) != 32 {
		t.Fatalf("txid: %v", err)
	}
	ls, err := bscript.NewFromHexString(strings.TrimSpace(u0.Script))
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	utxo := &bt.UTXO{
		TxID:          tid,
		Vout:          u0.Vout,
		Satoshis:      u0.Satoshis,
		LockingScript: ls,
	}
	network := strings.TrimSpace(in.Network)
	if network == "" {
		network = "testnet"
	}
	got, err := FreezeTBCWithSign(w.PrivKey, in.TbcNumber, in.LockTime, []*bt.UTXO{utxo}, network)
	if err != nil {
		t.Fatalf("FreezeTBCWithSign: %v", err)
	}
	want := strings.TrimSpace(piggyFreeze001ExpectedHex)
	if strings.EqualFold(got, want) {
		return
	}
	t.Fatalf("signed freeze raw mismatch (len got=%d want=%d)\ngot:  %s\nwant: %s", len(got), len(want), got, want)
}
