package contract

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

func TestNewPoolNFT2_Defaults(t *testing.T) {
	p := NewPoolNFT2(nil)
	if p.PoolVersion != poolNFT2Version {
		t.Fatalf("PoolVersion=%d", p.PoolVersion)
	}
	if p.Network != "mainnet" {
		t.Fatalf("Network=%q", p.Network)
	}
	if p.ServiceFeeRate != poolNFT2DefaultFeeBPS {
		t.Fatalf("ServiceFeeRate=%d", p.ServiceFeeRate)
	}
	if p.FtLpAmount.Cmp(big.NewInt(0)) != 0 {
		t.Fatal("FtLpAmount")
	}

	p2 := NewPoolNFT2(&PoolNFT2Config{ContractTxID: "aa", Network: "testnet"})
	if p2.ContractTxID != "aa" || p2.Network != "testnet" {
		t.Fatalf("cfg: %+v", p2)
	}
}

func TestPoolNFT2_InitCreate(t *testing.T) {
	p := NewPoolNFT2(nil)
	valid := "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234"
	if err := p.InitCreate(valid); err != nil {
		t.Fatal(err)
	}
	if p.FtAContractTxID != valid {
		t.Fatal("FtAContractTxID not set")
	}
	if err := p.InitCreate("not-a-hash"); err == nil {
		t.Fatal("expected error for invalid txid")
	}
	if err := p.InitCreate("gggg"); err == nil {
		t.Fatal("expected error")
	}
}

// 构造 9 个单字节 push，chunks[5..8] 分别为 0x19、0x02、0x01、0x01（费率 25、计划 2、双 bool）。
func TestParsePoolNftTapeExtra(t *testing.T) {
	// 01 <byte> × 9
	raw, err := hex.DecodeString("010001010102010301040119010201010101")
	if err != nil {
		t.Fatal(err)
	}
	sc := bscript.NewFromBytes(raw)
	ex, err := parsePoolNftTapeExtra(sc)
	if err != nil {
		t.Fatal(err)
	}
	if ex.ServiceFeeRate != 0x19 {
		t.Fatalf("ServiceFeeRate=%d want 25", ex.ServiceFeeRate)
	}
	if ex.LpPlan != 0x02 {
		t.Fatalf("LpPlan=%d", ex.LpPlan)
	}
	if !ex.WithLock || !ex.WithLockTime {
		t.Fatalf("bools: %+v", ex)
	}

	_, err = parsePoolNftTapeExtra(nil)
	if err == nil {
		t.Fatal("nil tape should error")
	}

	short := bscript.NewFromBytes([]byte{0x01, 0x00})
	exShort, _ := parsePoolNftTapeExtra(short)
	if exShort.ServiceFeeRate != 0 || exShort.LpPlan != 0 {
		t.Fatalf("short tape should yield zeros: %+v", exShort)
	}
}

func TestMustDecimalBig(t *testing.T) {
	if mustDecimalBig("").Cmp(big.NewInt(0)) != 0 {
		t.Fatal("empty")
	}
	if mustDecimalBig("  42 ").Cmp(big.NewInt(42)) != 0 {
		t.Fatal("42")
	}
	if mustDecimalBig("notnum").Cmp(big.NewInt(0)) != 0 {
		t.Fatal("invalid should be 0")
	}
}

func TestIsSHA256Hex(t *testing.T) {
	if !isSHA256Hex("abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789") {
		t.Fatal("valid lower")
	}
	if !isSHA256Hex("ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789") {
		t.Fatal("valid upper")
	}
	if isSHA256Hex("abcd") {
		t.Fatal("short")
	}
	if isSHA256Hex("gggg0123456789abcdef0123456789abcdef0123456789abcdef0123456789gg") {
		t.Fatal("invalid chars")
	}
}
