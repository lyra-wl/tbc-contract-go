// Package contract — piggyBank 定时锁脚本（对齐 tbc-contract/lib/contract/piggyBank.ts）。
package contract

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/libsv/go-bk/bec"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/go-bt/v2/unlocker"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

// GetPiggyBankCode 对齐 piggyBank.getPiggyBankCode。
func GetPiggyBankCode(address string, lockTime uint32) (*bscript.Script, error) {
	addr, err := bscript.NewAddressFromString(address)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, lockTime)
	lockHex := hex.EncodeToString(buf)
	asm := fmt.Sprintf(
		"OP_DUP OP_HASH160 %s OP_EQUALVERIFY OP_CHECKSIGVERIFY OP_6 OP_PUSH_META 24 OP_SPLIT OP_NIP OP_BIN2NUM ffffffff OP_BIN2NUM OP_NUMNOTEQUAL OP_1 OP_EQUALVERIFY %s OP_BIN2NUM OP_2 OP_PUSH_META OP_BIN2NUM OP_LESSTHANOREQUAL OP_1 OP_EQUAL",
		addr.PublicKeyHash, lockHex,
	)
	return bscript.NewFromASM(asm)
}

// FreezeTBC 对齐 piggyBank.freezeTBC，返回未签名交易 hex。
func FreezeTBC(address string, tbcNumber float64, lockTime uint32, utxos []*bt.UTXO) (string, error) {
	amt := util.ParseDecimalToBigInt(fmt.Sprintf("%g", tbcNumber), 6)
	if amt.Sign() <= 0 || !amt.IsUint64() {
		return "", fmt.Errorf("invalid tbc amount")
	}
	sat := amt.Uint64()
	script, err := GetPiggyBankCode(address, lockTime)
	if err != nil {
		return "", err
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: script, Satoshis: sat})
	if err := tx.ChangeToAddress(address, newFeeQuote80()); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

func addressFromPrivForNetwork(priv *bec.PrivateKey, network string) (*bscript.Address, error) {
	n := strings.TrimSpace(strings.ToLower(network))
	mainnet := n == "" || n == "mainnet"
	return bscript.NewAddressFromPublicKey(priv.PubKey(), mainnet)
}

// FreezeTBCWithSign 对齐 piggyBank._freezeTBC：在 FreezeTBC 基础上签名所有输入。
// network 用于推导付款/找零地址版本（testnet / mainnet），与私钥展示地址一致。
func FreezeTBCWithSign(priv *bec.PrivateKey, tbcNumber float64, lockTime uint32, utxos []*bt.UTXO, network string) (string, error) {
	if priv == nil {
		return "", fmt.Errorf("nil private key")
	}
	addr, err := addressFromPrivForNetwork(priv, network)
	if err != nil {
		return "", err
	}
	raw, err := FreezeTBC(addr.AddressString, tbcNumber, lockTime, utxos)
	if err != nil {
		return "", err
	}
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: priv}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// UnfreezeTBC 对齐 piggyBank.unfreezeTBC：设置 nLockTime 为当前链尖高度，输入 sequence=4294967294。
func UnfreezeTBC(address string, utxos []*bt.UTXO, network string) (string, error) {
	if network == "" {
		network = "mainnet"
	}
	headers, err := api.FetchBlockHeaders(network)
	if err != nil {
		return "", err
	}
	if len(headers) == 0 {
		return "", fmt.Errorf("no block headers")
	}
	var sum uint64
	for _, u := range utxos {
		sum += u.Satoshis
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	fee := uint64(80)
	if sum <= fee {
		return "", fmt.Errorf("insufficient amount for fee")
	}
	if err := tx.PayToAddress(address, sum-fee); err != nil {
		return "", err
	}
	if err := tx.ChangeToAddress(address, newFeeQuote80()); err != nil {
		return "", err
	}
	tx.LockTime = uint32(headers[0].Height)
	for _, in := range tx.Inputs {
		in.SequenceNumber = 4294967294
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// UnfreezeTBCWithSign 对齐 piggyBank._unfreezeTBC：在 UnfreezeTBC 基础上为所有输入签名。
func UnfreezeTBCWithSign(priv *bec.PrivateKey, utxos []*bt.UTXO, network string) (string, error) {
	if priv == nil {
		return "", fmt.Errorf("nil private key")
	}
	addr, err := addressFromPrivForNetwork(priv, network)
	if err != nil {
		return "", err
	}
	raw, err := UnfreezeTBC(addr.AddressString, utxos, network)
	if err != nil {
		return "", err
	}
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: priv}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// FetchTBCLockTime 对齐 piggyBank.fetchTBCLockTime（从锁定脚本倒数第 8 个 chunk 读 uint32 LE）。
// JS 侧校验 utxo.script.length == 106（hex 字符串长度，即 53 字节脚本）。
func FetchTBCLockTime(scriptHex string) (uint32, error) {
	if len(scriptHex) != 106 {
		return 0, fmt.Errorf("Invalid Piggy Bank script")
	}
	s, err := bscript.NewFromHexString(scriptHex)
	if err != nil {
		return 0, err
	}
	ch := s.Chunks()
	if len(ch) < 9 {
		return 0, fmt.Errorf("invalid Piggy Bank script")
	}
	idx := len(ch) - 8
	c := ch[idx]
	if len(c.Buf) < 4 {
		return 0, fmt.Errorf("invalid lock time chunk")
	}
	return binary.LittleEndian.Uint32(c.Buf[:4]), nil
}
