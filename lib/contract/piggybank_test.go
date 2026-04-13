package contract

import (
	"testing"

	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

func TestGetPiggyBankCode(t *testing.T) {
	addr := "1FhSD1YezTXbdRGWzNbNvUj6qeKQ6gZDMq"
	ok, _ := bscript.ValidateAddress(addr)
	if !ok {
		t.Skip("address validation")
	}
	s, err := GetPiggyBankCode(addr, 12345)
	if err != nil {
		t.Fatal(err)
	}
	hx := s.String()
	if len(hx) != 106 {
		t.Fatalf("want hex script len 106, got %d (bytes=%d)", len(hx), s.Len())
	}
	lt, err := FetchTBCLockTime(hx)
	if err != nil {
		t.Fatal(err)
	}
	if lt != 12345 {
		t.Fatalf("lockTime=%d", lt)
	}
}
