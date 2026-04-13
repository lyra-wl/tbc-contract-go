package contract

import (
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

func TestGetCoinMintCode(t *testing.T) {
	admin := "1FhSD1YezTXbdRGWzNbNvUj6qeKQ6gZDMq"
	recv := "1J37cpMhjhWJr6mzQh7i6Q2vx1wrQVgUMR"
	ok, _ := bscript.ValidateAddress(admin)
	if !ok {
		t.Skip("address validation")
	}
	codeHash := strings.Repeat("cd", 32)
	s, err := GetCoinMintCode(admin, recv, codeHash, 120)
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() < 500 {
		t.Fatalf("mint code unexpectedly short: %d", s.Len())
	}
}

func TestNewStableCoin(t *testing.T) {
	params := &FtParams{
		Name:    "USD Test",
		Symbol:  "USDT",
		Amount:  100000000,
		Decimal: 6,
	}
	sc, err := NewStableCoin(params)
	if err != nil {
		t.Fatalf("NewStableCoin: %v", err)
	}
	if sc.Name != "USD Test" || sc.Symbol != "USDT" {
		t.Errorf("Name/Symbol mismatch: got %s/%s", sc.Name, sc.Symbol)
	}
	if sc.Decimal != 6 || sc.TotalSupply != 100000000 {
		t.Errorf("Decimal/TotalSupply mismatch")
	}
}

func TestGetLockTimeFromTape(t *testing.T) {
	// Build a minimal tape with lockTime embedded
	ltBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(ltBuf, 1774410989)
	ltHex := hex.EncodeToString(ltBuf)
	// OP_FALSE OP_RETURN <lockTime> <marker>
	asmStr := "OP_FALSE OP_RETURN " + ltHex + " 4654617065"
	script, err := bscript.NewFromASM(asmStr)
	if err != nil {
		t.Fatalf("NewFromASM: %v", err)
	}
	lt := GetLockTimeFromTape(script)
	if lt != 1774410989 {
		t.Errorf("GetLockTimeFromTape = %d, want 1774410989", lt)
	}
}

func TestSetLockTimeInTape(t *testing.T) {
	ltBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(ltBuf, 500000000)
	ltHex := hex.EncodeToString(ltBuf)
	asmStr := "OP_FALSE OP_RETURN " + ltHex + " 4654617065"
	script, err := bscript.NewFromASM(asmStr)
	if err != nil {
		t.Fatalf("NewFromASM: %v", err)
	}

	newScript := SetLockTimeInTape(script, 0)
	newLt := GetLockTimeFromTape(newScript)
	if newLt != 0 {
		t.Errorf("SetLockTimeInTape(0) resulted in lockTime=%d, want 0", newLt)
	}
}

func TestParseDecimalToBigInt(t *testing.T) {
	tests := []struct {
		amount  string
		decimal int
		want    string
	}{
		{"1000", 6, "1000000000"},
		{"1.5", 6, "1500000"},
		{"0.001", 6, "1000"},
		{"100", 0, "100"},
	}
	for _, tt := range tests {
		result := ParseDecimalToBigInt(tt.amount, tt.decimal)
		want, _ := new(big.Int).SetString(tt.want, 10)
		if result.Cmp(want) != 0 {
			t.Errorf("ParseDecimalToBigInt(%q, %d) = %s, want %s", tt.amount, tt.decimal, result.String(), tt.want)
		}
	}
}

func TestGetAddressFromCode(t *testing.T) {
	result := GetAddressFromCode("")
	if result != "" {
		t.Errorf("empty code should return empty address, got %q", result)
	}

	result = GetAddressFromCode("0000")
	if result != "" {
		t.Errorf("short code should return empty address, got %q", result)
	}
}

func TestGetCoinNftTapeScript(t *testing.T) {
	data := &CoinNftData{
		NftName:         "USD Test NFT",
		NftSymbol:       "USDT NFT",
		Description:     "Test description",
		CoinDecimal:     6,
		CoinTotalSupply: "1000000",
	}
	script := GetCoinNftTapeScript(data)
	if script == nil || script.Len() == 0 {
		t.Fatal("GetCoinNftTapeScript returned nil/empty")
	}
}

func TestGetCoinNftHoldScript(t *testing.T) {
	addr := "1FhSD1YezTXbdRGWzNbNvUj6qeKQ6gZDMq"
	script := GetCoinNftHoldScript(addr, "USD Test NFT")
	if script == nil || script.Len() == 0 {
		t.Fatal("GetCoinNftHoldScript returned nil/empty")
	}
}

func TestGetCoinNftCode(t *testing.T) {
	txHash := strings.Repeat("ab", 32)
	script, err := GetCoinNftCode(txHash, 0)
	if err != nil {
		t.Fatalf("GetCoinNftCode: %v", err)
	}
	if script == nil || script.Len() < 100 {
		t.Fatalf("GetCoinNftCode returned script too short: %d", script.Len())
	}
}
