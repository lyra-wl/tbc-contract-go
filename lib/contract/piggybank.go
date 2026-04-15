// Package contract — piggyBank 定时锁脚本（对齐 tbc-contract/lib/contract/piggyBank.ts）。
package contract

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/libsv/go-bk/bec"
	"github.com/libsv/go-bk/crypto"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/go-bt/v2/sighash"
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

// piggyOrP2PKHPrefixUnlocker 用于花费 PiggyBank 锁定脚本：前缀与 P2PKH 相同（OP_DUP OP_HASH160 … OP_EQUALVERIFY OP_CHECKSIGVERIFY），
// IsP2PKH()/ScriptType 为 false 时 unlocker.Simple 会失败；按 PublicKeyHash 提取后与 JS setInputScript(sig+pubkey) 一致（见 nft.go p2pkhOrMintPrefixUnlocker）。
type piggyOrP2PKHPrefixUnlocker struct{ priv *bec.PrivateKey }

func (u *piggyOrP2PKHPrefixUnlocker) UnlockingScript(ctx context.Context, tx *bt.Tx, p bt.UnlockerParams) (*bscript.Script, error) {
	if p.SigHashFlags == 0 {
		p.SigHashFlags = sighash.AllForkID
	}
	prevScript := tx.Inputs[p.InputIdx].PreviousTxScript
	pkh, err := prevScript.PublicKeyHash()
	if err != nil {
		return nil, err
	}
	keyPKH := crypto.Hash160(u.priv.PubKey().SerialiseCompressed())
	if !bytes.Equal(pkh, keyPKH) {
		return bscript.NewFromBytes(nil), nil
	}
	sh, err := tx.CalcInputSignatureHash(p.InputIdx, p.SigHashFlags)
	if err != nil {
		return nil, err
	}
	sig, err := u.priv.Sign(sh)
	if err != nil {
		return nil, err
	}
	return bscript.NewP2PKHUnlockingScript(u.priv.PubKey().SerialiseCompressed(), sig.Serialise(), p.SigHashFlags)
}

type piggyOrP2PKHPrefixUnlockerGetter struct{ priv *bec.PrivateKey }

func (g *piggyOrP2PKHPrefixUnlockerGetter) Unlocker(ctx context.Context, lockingScript *bscript.Script) (bt.Unlocker, error) {
	return &piggyOrP2PKHPrefixUnlocker{priv: g.priv}, nil
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
	// 线格式输入不含上一笔 locking script；反序列化后需写回，否则 FillAllInputs 无法按 P2PKH 签名。
	applyUtxosToTxInputs(tx, utxos)
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
	applyUtxosToTxInputs(tx, utxos)
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &piggyOrP2PKHPrefixUnlockerGetter{priv: priv}); err != nil {
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

func applyUtxosToTxInputs(tx *bt.Tx, utxos []*bt.UTXO) {
	for i := range tx.Inputs {
		if i >= len(utxos) {
			break
		}
		tx.Inputs[i].PreviousTxScript = utxos[i].LockingScript
		tx.Inputs[i].PreviousTxSatoshis = utxos[i].Satoshis
	}
}
