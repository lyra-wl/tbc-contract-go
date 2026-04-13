// HTLC 交易使用 newFTTx()（version=10），以便在 TBC 测试网/主网通过 api.BroadcastTXRaw 广播；
// tbc-lib-js 默认 version=1，在相同节点上可能被拒绝。
package contract

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/libsv/go-bk/bec"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/go-bt/v2/sighash"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

// DeployHTLC 构建未签名 HTLC 部署交易（与 tbc-contract/lib/contract/htlc.js deployHTLC 一致）
func DeployHTLC(sender, receiver, hashlock string, timelock int, amountTBC float64, utxo *bt.UTXO) (string, error) {
	if err := validateP2PKHAddress(sender); err != nil {
		return "", err
	}
	if err := validateP2PKHAddress(receiver); err != nil {
		return "", err
	}
	if !util.IsValidSHA256Hash(hashlock) {
		return "", fmt.Errorf("Invalid hashlock")
	}
	if timelock <= 0 {
		return "", fmt.Errorf("Invalid timelock")
	}
	senderAddr, _ := bscript.NewAddressFromString(sender)
	receiverAddr, _ := bscript.NewAddressFromString(receiver)
	senderPubHash := senderAddr.PublicKeyHash
	receiverPubHash := receiverAddr.PublicKeyHash
	script, err := htlcLockingScript(senderPubHash, receiverPubHash, hashlock, timelock)
	if err != nil {
		return "", err
	}
	amt := util.ParseDecimalToBigInt(fmt.Sprintf("%g", amountTBC), 6)
	if amt.Sign() <= 0 {
		return "", fmt.Errorf("invalid amount")
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxo); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: script, Satoshis: amt.Uint64()})
	if err := tx.ChangeToAddress(sender, newFeeQuote80()); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// Withdraw 构建未签名提取交易（receiver 路径）
func Withdraw(receiver string, htlcUtxo *bt.UTXO) (string, error) {
	if err := validateP2PKHAddress(receiver); err != nil {
		return "", err
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(htlcUtxo); err != nil {
		return "", err
	}
	addr, err := bscript.NewAddressFromString(receiver)
	if err != nil {
		return "", err
	}
	pkScript, err := bscript.NewP2PKHFromPubKeyHash(hexDecode(addr.PublicKeyHash))
	if err != nil {
		return "", err
	}
	outAmt := htlcUtxo.Satoshis - 80
	if htlcUtxo.Satoshis < 80 {
		return "", fmt.Errorf("htlc utxo too small for fee")
	}
	tx.AddOutput(&bt.Output{LockingScript: pkScript, Satoshis: outAmt})
	return hex.EncodeToString(tx.Bytes()), nil
}

// Refund 构建未签名退款交易（sender 路径，需 timelock 到期）
func Refund(sender string, htlcUtxo *bt.UTXO, timelock int) (string, error) {
	if err := validateP2PKHAddress(sender); err != nil {
		return "", err
	}
	if timelock <= 0 {
		return "", fmt.Errorf("Invalid timelock")
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(htlcUtxo); err != nil {
		return "", err
	}
	addr, err := bscript.NewAddressFromString(sender)
	if err != nil {
		return "", err
	}
	pkScript, err := bscript.NewP2PKHFromPubKeyHash(hexDecode(addr.PublicKeyHash))
	if err != nil {
		return "", err
	}
	outAmt := htlcUtxo.Satoshis - 80
	if htlcUtxo.Satoshis < 80 {
		return "", fmt.Errorf("htlc utxo too small for fee")
	}
	tx.AddOutput(&bt.Output{LockingScript: pkScript, Satoshis: outAmt})
	if len(tx.Inputs) > 0 {
		tx.Inputs[0].SequenceNumber = 4294967294
	}
	tx.LockTime = uint32(timelock)
	return hex.EncodeToString(tx.Bytes()), nil
}

// FillSigDeploy 将 deploy 交易输入 0 填入 P2PKH 解锁脚本（sig + pubkey）
func FillSigDeploy(deployHTLCTxRaw, sigHex, publicKeyHex string) (string, error) {
	if !util.IsValidHexString(deployHTLCTxRaw) {
		return "", fmt.Errorf("Invalid DeployHTLCTxRaw hex string")
	}
	if _, err := bec.ParsePubKey(hexDecode(publicKeyHex), bec.S256()); err != nil {
		return "", fmt.Errorf("Invalid PublicKey")
	}
	if !util.IsValidHexString(sigHex) {
		return "", fmt.Errorf("Invalid Signature")
	}
	tx, err := bt.NewTxFromString(deployHTLCTxRaw)
	if err != nil {
		return "", err
	}
	tx.Version = 10
	asm := fmt.Sprintf("%s %s", sigHex, publicKeyHex)
	us, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, us); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// FillSigWithdraw 填入提取解锁脚本
func FillSigWithdraw(withdrawTxRaw, secret, sigHex, publicKeyHex string) (string, error) {
	if !util.IsValidHexString(withdrawTxRaw) {
		return "", fmt.Errorf("Invalid WithdrawTxRaw hex string")
	}
	if _, err := bec.ParsePubKey(hexDecode(publicKeyHex), bec.S256()); err != nil {
		return "", fmt.Errorf("Invalid PublicKey")
	}
	if !util.IsValidHexString(sigHex) {
		return "", fmt.Errorf("Invalid Signature")
	}
	tx, err := bt.NewTxFromString(withdrawTxRaw)
	if err != nil {
		return "", err
	}
	tx.Version = 10
	asm := fmt.Sprintf("%s %s %s 1", sigHex, publicKeyHex, secret)
	us, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, us); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// FillSigRefund 填入退款解锁脚本
func FillSigRefund(refundTxRaw, sigHex, publicKeyHex string) (string, error) {
	if !util.IsValidHexString(refundTxRaw) {
		return "", fmt.Errorf("Invalid RefundTxRaw hex string")
	}
	if _, err := bec.ParsePubKey(hexDecode(publicKeyHex), bec.S256()); err != nil {
		return "", fmt.Errorf("Invalid PublicKey")
	}
	if !util.IsValidHexString(sigHex) {
		return "", fmt.Errorf("Invalid Signature")
	}
	tx, err := bt.NewTxFromString(refundTxRaw)
	if err != nil {
		return "", err
	}
	tx.Version = 10
	asm := fmt.Sprintf("%s %s 0", sigHex, publicKeyHex)
	us, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, us); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// DeployHTLCWithSign 已签名部署交易（与 deployHTLCWithSign 一致）
func DeployHTLCWithSign(sender, receiver, hashlock string, timelock int, amountTBC float64, utxo *bt.UTXO, privKey *bec.PrivateKey) (string, error) {
	if err := validateP2PKHAddress(sender); err != nil {
		return "", err
	}
	if err := validateP2PKHAddress(receiver); err != nil {
		return "", err
	}
	if !util.IsValidSHA256Hash(hashlock) {
		return "", fmt.Errorf("Invalid hashlock")
	}
	if timelock < 0 {
		return "", fmt.Errorf("Invalid timelock")
	}
	senderAddr, _ := bscript.NewAddressFromString(sender)
	receiverAddr, _ := bscript.NewAddressFromString(receiver)
	script, err := htlcLockingScript(senderAddr.PublicKeyHash, receiverAddr.PublicKeyHash, hashlock, timelock)
	if err != nil {
		return "", err
	}
	amt := util.ParseDecimalToBigInt(fmt.Sprintf("%g", amountTBC), 6)
	tx := newFTTx()
	if err := tx.FromUTXOs(utxo); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: script, Satoshis: amt.Uint64()})
	if err := tx.ChangeToAddress(sender, newFeeQuote80()); err != nil {
		return "", err
	}
	if err := signP2PKHInput(tx, privKey, 0); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// WithdrawWithSign 已签名提取（receiver + secret）
func WithdrawWithSign(privKey *bec.PrivateKey, receiver string, htlcUtxo *bt.UTXO, secret string) (string, error) {
	if err := validateP2PKHAddress(receiver); err != nil {
		return "", err
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(htlcUtxo); err != nil {
		return "", err
	}
	addr, err := bscript.NewAddressFromString(receiver)
	if err != nil {
		return "", err
	}
	pkScript, err := bscript.NewP2PKHFromPubKeyHash(hexDecode(addr.PublicKeyHash))
	if err != nil {
		return "", err
	}
	outAmt := htlcUtxo.Satoshis - 80
	if htlcUtxo.Satoshis < 80 {
		return "", fmt.Errorf("htlc utxo too small for fee")
	}
	tx.AddOutput(&bt.Output{LockingScript: pkScript, Satoshis: outAmt})
	sh, err := tx.CalcInputSignatureHash(0, sighash.AllForkID)
	if err != nil {
		return "", err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return "", err
	}
	sigBytes := sig.Serialise()
	sigBytes = append(sigBytes, byte(sighash.AllForkID))
	pubHex := hex.EncodeToString(privKey.PubKey().SerialiseCompressed())
	sigHex := hex.EncodeToString(sigBytes)
	asm := fmt.Sprintf("%s %s %s OP_TRUE", sigHex, pubHex, secret)
	us, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, us); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// RefundWithSign 已签名退款
func RefundWithSign(sender string, htlcUtxo *bt.UTXO, privKey *bec.PrivateKey, timelock int) (string, error) {
	if err := validateP2PKHAddress(sender); err != nil {
		return "", err
	}
	if timelock <= 0 {
		return "", fmt.Errorf("Invalid timelock")
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(htlcUtxo); err != nil {
		return "", err
	}
	addr, err := bscript.NewAddressFromString(sender)
	if err != nil {
		return "", err
	}
	pkScript, err := bscript.NewP2PKHFromPubKeyHash(hexDecode(addr.PublicKeyHash))
	if err != nil {
		return "", err
	}
	outAmt := htlcUtxo.Satoshis - 80
	if htlcUtxo.Satoshis < 80 {
		return "", fmt.Errorf("htlc utxo too small for fee")
	}
	tx.AddOutput(&bt.Output{LockingScript: pkScript, Satoshis: outAmt})
	if len(tx.Inputs) > 0 {
		tx.Inputs[0].SequenceNumber = 4294967294
	}
	tx.LockTime = uint32(timelock)
	sh, err := tx.CalcInputSignatureHash(0, sighash.AllForkID)
	if err != nil {
		return "", err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return "", err
	}
	sigBytes := sig.Serialise()
	sigBytes = append(sigBytes, byte(sighash.AllForkID))
	pubHex := hex.EncodeToString(privKey.PubKey().SerialiseCompressed())
	sigHex := hex.EncodeToString(sigBytes)
	asm := fmt.Sprintf("%s %s OP_FALSE", sigHex, pubHex)
	us, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, us); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

func htlcLockingScript(senderPubHash, receiverPubHash, hashlock string, timelock int) (*bscript.Script, error) {
	tb := make([]byte, 4)
	binary.LittleEndian.PutUint32(tb, uint32(timelock))
	timelockHex := hex.EncodeToString(tb)
	asm := fmt.Sprintf(
		"OP_IF OP_SHA256 %s OP_EQUALVERIFY OP_DUP OP_HASH160 %s OP_ELSE %s OP_BIN2NUM OP_2 OP_PUSH_META OP_BIN2NUM OP_2DUP OP_GREATERTHAN OP_NOTIF OP_2DUP 0065cd1d OP_GREATERTHANOREQUAL OP_IF 0065cd1d OP_GREATERTHANOREQUAL OP_VERIFY OP_LESSTHANOREQUAL OP_ELSE OP_2DROP OP_DROP OP_TRUE OP_ENDIF OP_ELSE OP_FALSE OP_ENDIF OP_VERIFY OP_6 OP_PUSH_META 24 OP_SPLIT OP_NIP OP_BIN2NUM ffffffff OP_NUMNOTEQUAL OP_VERIFY OP_DUP OP_HASH160 %s OP_ENDIF OP_EQUALVERIFY OP_CHECKSIG",
		hashlock, receiverPubHash, timelockHex, senderPubHash,
	)
	return bscript.NewFromASM(asm)
}

func validateP2PKHAddress(addr string) error {
	ok, _ := bscript.ValidateAddress(addr)
	if !ok {
		return fmt.Errorf("Invalid sender or receiver address")
	}
	return nil
}
