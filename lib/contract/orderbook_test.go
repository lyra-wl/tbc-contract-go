package contract

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewOrderBook(t *testing.T) {
	ob := NewOrderBook()
	if ob == nil {
		t.Fatal("NewOrderBook returned nil")
	}
	if ob.ContractVersion != 1 {
		t.Errorf("ContractVersion = %d, want 1", ob.ContractVersion)
	}
	if ob.BuyCodeDust != 300 {
		t.Errorf("BuyCodeDust = %d, want 300", ob.BuyCodeDust)
	}
}

func TestBuildOrderDataHex_RoundTrip(t *testing.T) {
	ob := &OrderBook{
		HoldAddress:        "1FhSD1YezTXbdRGWzNbNvUj6qeKQ6gZDMq",
		SaleVolume:         1000000,
		FeeRate:            5000,
		UnitPrice:          200000,
		FtAContractPartial: strings.Repeat("ab", 32),
		FtAContractID:      strings.Repeat("cd", 32),
	}
	dataHex, err := ob.buildOrderDataHex()
	if err != nil {
		t.Fatalf("buildOrderDataHex: %v", err)
	}
	if len(dataHex) != orderDataEncodedLen*2 {
		t.Fatalf("dataHex length = %d, want %d", len(dataHex), orderDataEncodedLen*2)
	}

	data, err := hex.DecodeString(dataHex)
	if err != nil {
		t.Fatal(err)
	}

	if data[0] != 0x14 {
		t.Errorf("marker[0] = 0x%02x, want 0x14", data[0])
	}
	gotSale := binary.LittleEndian.Uint64(data[22:30])
	if gotSale != 1000000 {
		t.Errorf("SaleVolume = %d, want 1000000", gotSale)
	}
	gotFeeRate := binary.LittleEndian.Uint64(data[64:72])
	if gotFeeRate != 5000 {
		t.Errorf("FeeRate = %d, want 5000", gotFeeRate)
	}
	gotUnitPrice := binary.LittleEndian.Uint64(data[73:81])
	if gotUnitPrice != 200000 {
		t.Errorf("UnitPrice = %d, want 200000", gotUnitPrice)
	}
}

func TestGetOrderData_Roundtrip(t *testing.T) {
	ob := &OrderBook{
		HoldAddress:        "1FhSD1YezTXbdRGWzNbNvUj6qeKQ6gZDMq",
		SaleVolume:         1000000,
		FeeRate:            5000,
		UnitPrice:          200000,
		FtAContractPartial: strings.Repeat("ab", 32),
		FtAContractID:      strings.Repeat("cd", 32),
	}
	dataHex, err := ob.buildOrderDataHex()
	if err != nil {
		t.Fatal(err)
	}
	codeHex := sellOrderTemplateHex + dataHex
	od, err := GetOrderData(codeHex, true)
	if err != nil {
		t.Fatalf("GetOrderData: %v", err)
	}
	if od.HoldAddress != ob.HoldAddress {
		t.Errorf("HoldAddress = %q, want %q", od.HoldAddress, ob.HoldAddress)
	}
	if od.SaleVolume != 1000000 {
		t.Errorf("SaleVolume = %d, want 1000000", od.SaleVolume)
	}
	if od.FeeRate != 5000 {
		t.Errorf("FeeRate = %d, want 5000", od.FeeRate)
	}
	if od.UnitPrice != 200000 {
		t.Errorf("UnitPrice = %d, want 200000", od.UnitPrice)
	}
	if od.FtPartialHash != strings.Repeat("ab", 32) {
		t.Errorf("FtPartialHash mismatch")
	}
	if od.FtID != strings.Repeat("cd", 32) {
		t.Errorf("FtID mismatch")
	}
}

func TestUpdateSaleVolume(t *testing.T) {
	ob := &OrderBook{
		HoldAddress:        "1FhSD1YezTXbdRGWzNbNvUj6qeKQ6gZDMq",
		SaleVolume:         1000000,
		FeeRate:            5000,
		UnitPrice:          200000,
		FtAContractPartial: strings.Repeat("ab", 32),
		FtAContractID:      strings.Repeat("cd", 32),
	}
	dataHex, _ := ob.buildOrderDataHex()
	codeHex := sellOrderTemplateHex + dataHex

	updatedHex, err := UpdateSaleVolume(codeHex, 500000)
	if err != nil {
		t.Fatalf("UpdateSaleVolume: %v", err)
	}

	od, err := GetOrderData(updatedHex, true)
	if err != nil {
		t.Fatal(err)
	}
	if od.SaleVolume != 500000 {
		t.Errorf("SaleVolume = %d, want 500000", od.SaleVolume)
	}
	if od.UnitPrice != 200000 {
		t.Errorf("UnitPrice changed to %d, want unchanged 200000", od.UnitPrice)
	}
}

func TestGetSellOrderCode(t *testing.T) {
	ob := &OrderBook{
		HoldAddress:        "1FhSD1YezTXbdRGWzNbNvUj6qeKQ6gZDMq",
		SaleVolume:         1000000,
		FeeRate:            5000,
		UnitPrice:          200000,
		FtAContractPartial: strings.Repeat("ab", 32),
		FtAContractID:      strings.Repeat("cd", 32),
	}
	script, err := ob.GetSellOrderCode()
	if err != nil {
		t.Fatalf("GetSellOrderCode: %v", err)
	}
	expectedMinLen := len(sellOrderTemplateHex)/2 + orderDataEncodedLen
	if script.Len() < expectedMinLen {
		t.Errorf("sell order code length = %d, want >= %d", script.Len(), expectedMinLen)
	}
}

func TestGetBuyOrderCode(t *testing.T) {
	ob := &OrderBook{
		HoldAddress:        "1FhSD1YezTXbdRGWzNbNvUj6qeKQ6gZDMq",
		SaleVolume:         1000000,
		FeeRate:            5000,
		UnitPrice:          200000,
		FtAContractPartial: strings.Repeat("ab", 32),
		FtAContractID:      strings.Repeat("cd", 32),
	}
	script, err := ob.GetBuyOrderCode()
	if err != nil {
		t.Fatalf("GetBuyOrderCode: %v", err)
	}
	expectedMinLen := len(buyOrderTemplateHex)/2 + orderDataEncodedLen
	if script.Len() < expectedMinLen {
		t.Errorf("buy order code length = %d, want >= %d", script.Len(), expectedMinLen)
	}
}

func TestIsValidSHA256Hex(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{strings.Repeat("ab", 32), true},
		{strings.Repeat("AB", 32), true},
		{"", false},
		{strings.Repeat("ab", 16), false},
		{strings.Repeat("gg", 32), false},
	}
	for _, tt := range tests {
		got := isValidSHA256Hex(tt.input)
		if got != tt.want {
			t.Errorf("isValidSHA256Hex(%q) = %v, want %v", tt.input[:min(16, len(tt.input))], got, tt.want)
		}
	}
}

func TestPlaceHolderP2PKHOutput(t *testing.T) {
	script, err := PlaceHolderP2PKHOutput()
	if err != nil {
		t.Fatalf("PlaceHolderP2PKHOutput: %v", err)
	}
	if script == nil || script.Len() == 0 {
		t.Fatal("PlaceHolderP2PKHOutput returned empty script")
	}
}

func TestObGetSize(t *testing.T) {
	small := obGetSize(100)
	if len(small) != 1 || small[0] != 100 {
		t.Errorf("obGetSize(100) = %v, want [100]", small)
	}
	large := obGetSize(300)
	if len(large) != 2 {
		t.Errorf("obGetSize(300) should return 2 bytes, got %d", len(large))
	}
	got := binary.LittleEndian.Uint16(large)
	if got != 300 {
		t.Errorf("obGetSize(300) = %d, want 300", got)
	}
}

func TestObGetLengthHex(t *testing.T) {
	short := obGetLengthHex(50)
	if len(short) != 1 || short[0] != 50 {
		t.Errorf("obGetLengthHex(50) = %v, want [50]", short)
	}
	medium := obGetLengthHex(100)
	if len(medium) != 2 || medium[0] != 0x4c || medium[1] != 100 {
		t.Errorf("obGetLengthHex(100) = %v, want [0x4c, 100]", medium)
	}
	long := obGetLengthHex(300)
	if len(long) != 3 || long[0] != 0x4d {
		t.Errorf("obGetLengthHex(300) = %v, want [0x4d, ...]", long)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
