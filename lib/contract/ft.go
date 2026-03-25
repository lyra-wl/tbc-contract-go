// Package contract 提供 TBC 合约的 Go 实现
// 对应 tbc-contract 的 lib/contract
package contract

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"

	"github.com/libsv/go-bk/bec"
	"github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/go-bt/v2/sighash"
	"github.com/sCrypt-Inc/go-bt/v2/unlocker"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

// FtParams 新建 FT 时的参数
type FtParams struct {
	Name    string
	Symbol  string
	Amount  int64
	Decimal int
}

// FtInfo FT 代币信息，用于 Initialize
type FtInfo struct {
	Name        string
	Symbol      string
	Decimal     int
	TotalSupply int64
	CodeScript  string
	TapeScript  string
}

// FT 同质化代币合约
type FT struct {
	Name         string
	Symbol       string
	Decimal      int
	TotalSupply  int64
	CodeScript   string
	TapeScript   string
	ContractTxid string
}

// NewFT 创建 FT 实例，支持从 txid 或参数创建
func NewFT(txidOrParams interface{}) (*FT, error) {
	ft := &FT{}
	switch v := txidOrParams.(type) {
	case string:
		ft.ContractTxid = v
	case *FtParams:
		if v.Amount <= 0 {
			return nil, fmt.Errorf("amount must be a natural number")
		}
		if v.Decimal <= 0 || v.Decimal > 18 {
			return nil, fmt.Errorf("decimal must be 1-18")
		}
		maxAmount := int64(math.Floor(21 * math.Pow(10, float64(14-v.Decimal))))
		if v.Amount > maxAmount {
			return nil, fmt.Errorf("when decimal is %d, max amount cannot exceed %d", v.Decimal, maxAmount)
		}
		ft.Name = v.Name
		ft.Symbol = v.Symbol
		ft.Decimal = v.Decimal
		ft.TotalSupply = v.Amount
	default:
		return nil, fmt.Errorf("invalid constructor arguments")
	}
	return ft, nil
}

// Initialize 用 FtInfo 初始化
func (f *FT) Initialize(info *FtInfo) {
	f.Name = info.Name
	f.Symbol = info.Symbol
	f.Decimal = info.Decimal
	f.TotalSupply = info.TotalSupply
	f.CodeScript = info.CodeScript
	f.TapeScript = info.TapeScript
}

// MintFT 铸造新 FT，返回 [txSourceRaw, txMintRaw]
func (f *FT) MintFT(privKey *bec.PrivateKey, addressTo string, utxo *bt.UTXO) ([]string, error) {
	totalSupply := new(big.Int).Mul(
		big.NewInt(f.TotalSupply),
		new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(f.Decimal)), nil),
	)
	tapeAmount := writeTapeAmount(totalSupply)

	nameHex := hex.EncodeToString([]byte(f.Name))
	symbolHex := hex.EncodeToString([]byte(f.Symbol))
	decimalHex := fmt.Sprintf("%02x", f.Decimal)

	tapeASM := fmt.Sprintf("OP_FALSE OP_RETURN %s %s %s %s 4654617065", tapeAmount, decimalHex, nameHex, symbolHex)
	tapeScript, err := bscript.NewFromASM(tapeASM)
	if err != nil {
		return nil, err
	}
	tapeSize := len(tapeScript.Bytes())

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		return nil, err
	}
	pubKeyHash := addr.PublicKeyHash
	flagHex := hex.EncodeToString([]byte("for ft mint"))

	sourceOutputScript, _ := bscript.NewFromASM(fmt.Sprintf("OP_DUP OP_HASH160 %s OP_EQUALVERIFY OP_CHECKSIG OP_RETURN %s", pubKeyHash, flagHex))

	txSource := bt.NewTx()
	_ = txSource.FromUTXOs(utxo)
	txSource.AddOutput(&bt.Output{LockingScript: sourceOutputScript, Satoshis: 9900})
	txSource.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	feeQuote := newFeeQuote80()
	_ = txSource.ChangeToAddress(addr.AddressString, feeQuote)

	ctx := context.Background()
	if err := txSource.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return nil, err
	}

	txSourceRaw := hex.EncodeToString(txSource.Bytes())

	codeScript := getFTmintCode(txSource.TxID(), 0, addressTo, tapeSize)
	f.CodeScript = hex.EncodeToString(codeScript.Bytes())
	f.TapeScript = hex.EncodeToString(tapeScript.Bytes())

	txMint := bt.NewTx()
	_ = txMint.From(txSource.TxID(), 0, sourceOutputScript.String(), 9900)
	txMint.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	txMint.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})
	_ = txMint.ChangeToAddress(addr.AddressString, feeQuote)

	if err := txMint.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return nil, err
	}

	f.ContractTxid = txMint.TxID()
	return []string{txSourceRaw, hex.EncodeToString(txMint.Bytes())}, nil
}

// Transfer 转移 FT
func (f *FT) Transfer(privKey *bec.PrivateKey, addressTo string, ftAmount float64,
	ftutxos []*bt.FtUTXO, utxo *bt.UTXO, preTX []*bt.Tx, prepreTxData []string, tbcAmountSat uint64) (string, error) {

	addr, _ := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	addressFrom := addr.AddressString

	if ftAmount < 0 {
		return "", fmt.Errorf("invalid amount input")
	}
	amountBN := new(big.Float).Mul(big.NewFloat(ftAmount), big.NewFloat(math.Pow10(f.Decimal)))
	amountBNInt, _ := amountBN.Int(nil)

	tapeAmountSet := make([]*big.Int, len(ftutxos))
	tapeAmountSum := new(big.Int)
	for i, fu := range ftutxos {
		b, _ := new(big.Int).SetString(fu.FtBalance, 10)
		tapeAmountSet[i] = b
		tapeAmountSum.Add(tapeAmountSum, b)
	}
	if amountBNInt.Cmp(tapeAmountSum) > 0 {
		return "", fmt.Errorf("insufficient balance, please add more FT UTXOs")
	}
	if f.Decimal > 18 {
		return "", fmt.Errorf("decimal cannot exceed 18")
	}
	maxAmount := math.Floor(21 * math.Pow(10, float64(14-f.Decimal)))
	if ftAmount > maxAmount {
		return "", fmt.Errorf("when decimal is %d, max amount cannot exceed %.0f", f.Decimal, maxAmount)
	}

	amountHex, changeHex := BuildTapeAmount(amountBNInt, tapeAmountSet)

	ftUTXOs, _ := util.FtUTXOsToUTXOs(ftutxos)
	tx := bt.NewTx()
	_ = tx.FromUTXOs(ftUTXOs...)
	_ = tx.FromUTXOs(utxo)

	codeScript := BuildFTtransferCode(f.CodeScript, addressTo)
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	tapeScript := BuildFTtransferTape(f.TapeScript, amountHex)
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	if tbcAmountSat > 0 {
		tx.To(addressTo, tbcAmountSat)
	}
	if amountBNInt.Cmp(tapeAmountSum) < 0 {
		changeCode := BuildFTtransferCode(f.CodeScript, addressFrom)
		tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: 500})
		changeTape := BuildFTtransferTape(f.TapeScript, changeHex)
		tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
	}

	feeQuote := newFeeQuote80()
	_ = tx.ChangeToAddress(addressFrom, feeQuote)

	ftUnlockScripts := make([]*bscript.Script, len(ftutxos))
	for i := range ftutxos {
		us, err := f.getFTunlock(privKey, tx, preTX[i], prepreTxData[i], i, int(ftutxos[i].Vout))
		if err != nil {
			return "", err
		}
		ftUnlockScripts[i] = us
	}

	ctx := context.Background()
	ug := newFTTransferUnlockerGetter(ftUnlockScripts, privKey)
	if err := tx.FillAllInputs(ctx, ug); err != nil {
		return "", err
	}

	return hex.EncodeToString(tx.Bytes()), nil
}

// ftTransferUnlockerGetter 用于 FT 转移，混合 FT 自定义解锁和 P2PKH 标准解锁
type ftTransferUnlockerGetter struct {
	ftScripts []*bscript.Script
	privKey   *bec.PrivateKey
	callIdx   int
}

func newFTTransferUnlockerGetter(ftScripts []*bscript.Script, privKey *bec.PrivateKey) *ftTransferUnlockerGetter {
	return &ftTransferUnlockerGetter{ftScripts: ftScripts, privKey: privKey}
}

func (g *ftTransferUnlockerGetter) Unlocker(ctx context.Context, lockingScript *bscript.Script) (bt.Unlocker, error) {
	idx := g.callIdx
	g.callIdx++
	if idx < len(g.ftScripts) {
		return &fixedScriptUnlocker{script: g.ftScripts[idx]}, nil
	}
	return &unlocker.Simple{PrivateKey: g.privKey}, nil
}

type fixedScriptUnlocker struct {
	script *bscript.Script
}

func (u *fixedScriptUnlocker) UnlockingScript(ctx context.Context, tx *bt.Tx, params bt.UnlockerParams) (*bscript.Script, error) {
	return u.script, nil
}

func (f *FT) getFTunlock(privKey *bec.PrivateKey, tx *bt.Tx, preTX *bt.Tx, prepreTxData string, inputIdx, preTxVout int) (*bscript.Script, error) {
	pretxdata, err := bt.GetPreTxdata(preTX, preTxVout)
	if err != nil {
		return nil, err
	}
	currenttxdata, err := bt.GetCurrentTxdata(tx, inputIdx)
	if err != nil {
		return nil, err
	}
	sh, err := tx.CalcInputSignatureHash(uint32(inputIdx), sighash.AllForkID)
	if err != nil {
		return nil, err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return nil, err
	}
	sigBytes := sig.Serialise()
	sigHex := fmt.Sprintf("%02x%s", len(sigBytes), hex.EncodeToString(sigBytes))
	pubKey := privKey.PubKey().SerialiseCompressed()
	pubKeyHex := fmt.Sprintf("%02x%s", len(pubKey), hex.EncodeToString(pubKey))

	unlockHex := currenttxdata + prepreTxData + sigHex + pubKeyHex + pretxdata
	unlockScript, err := bscript.NewFromHexString(unlockHex)
	if err != nil {
		return nil, err
	}
	return unlockScript, nil
}

// BuildTapeAmount 构建 amount 和 change 的 hex，与 JS FT.buildTapeAmount 一致
func BuildTapeAmount(amountBN *big.Int, tapeAmountSet []*big.Int) (amountHex, changeHex string) {
	zero := big.NewInt(0)
	amountBuf := make([]byte, 48)
	changeBuf := make([]byte, 48)
	remain := new(big.Int).Set(amountBN)
	i := 0

	for i < 6 && i < len(tapeAmountSet) {
		slot := tapeAmountSet[i]
		if slot == nil || remain.Cmp(zero) <= 0 {
			break
		}
		if slot.Cmp(remain) < 0 {
			writeUint64LE(amountBuf[i*8:], slot)
			writeUint64LE(changeBuf[i*8:], zero)
			remain.Sub(remain, slot)
		} else {
			writeUint64LE(amountBuf[i*8:], remain)
			writeUint64LE(changeBuf[i*8:], new(big.Int).Sub(slot, remain))
			remain = zero
		}
		i++
	}
	for ; i < 6 && i < len(tapeAmountSet); i++ {
		if tapeAmountSet[i] != nil && tapeAmountSet[i].Cmp(zero) != 0 {
			writeUint64LE(amountBuf[i*8:], zero)
			writeUint64LE(changeBuf[i*8:], tapeAmountSet[i])
		} else {
			writeUint64LE(amountBuf[i*8:], zero)
			writeUint64LE(changeBuf[i*8:], zero)
		}
	}
	return hex.EncodeToString(amountBuf), hex.EncodeToString(changeBuf)
}

func writeUint64LE(buf []byte, n *big.Int) {
	u := n.Uint64()
	binary.LittleEndian.PutUint64(buf, u)
}

// BuildFTtransferCode 构建转移用的 code script
func BuildFTtransferCode(codeHex, addressOrHash string) *bscript.Script {
	codeBuf, _ := hex.DecodeString(codeHex)
	var hashBuf []byte
	ok, _ := bscript.ValidateAddress(addressOrHash)
	if ok {
		addr, _ := bscript.NewAddressFromString(addressOrHash)
		hashBuf = make([]byte, 21)
		copy(hashBuf, hexDecode(addr.PublicKeyHash))
		hashBuf[20] = 0x00
	} else {
		if len(addressOrHash) != 40 {
			panic("invalid address or hash")
		}
		hashBuf = hexDecode(addressOrHash + "01")
	}
	if len(codeBuf) >= 1558 {
		copy(codeBuf[1537:1558], hashBuf)
	}
	return bscript.NewFromBytes(codeBuf)
}

// BuildFTtransferTape 构建转移用的 tape script
func BuildFTtransferTape(tapeHex, amountHex string) *bscript.Script {
	amountBuf, _ := hex.DecodeString(amountHex)
	tapeBuf, _ := hex.DecodeString(tapeHex)
	copy(tapeBuf[3:51], amountBuf[:48])
	return bscript.NewFromBytes(tapeBuf)
}

// GetBalanceFromTape 从 tape 提取余额
func GetBalanceFromTape(tapeHex string) *big.Int {
	s := util.GetFtBalanceFromTape(tapeHex)
	b, _ := new(big.Int).SetString(s, 10)
	return b
}

func writeTapeAmount(totalSupply *big.Int) string {
	buf := make([]byte, 48)
	for i := 0; i < 6; i++ {
		word := new(big.Int).Rsh(totalSupply, uint(i*64))
		word = word.And(word, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1)))
		binary.LittleEndian.PutUint64(buf[i*8:], word.Uint64())
	}
	return hex.EncodeToString(buf)
}

func getFTmintCode(txid string, vout int, address string, tapeSize int) *bscript.Script {
	txidBytes, _ := hex.DecodeString(txid)
	utxoBuf := make([]byte, 36)
	for i := 0; i < 32; i++ {
		utxoBuf[i] = txidBytes[31-i]
	}
	binary.LittleEndian.PutUint32(utxoBuf[32:], uint32(vout))
	utxoHex := hex.EncodeToString(utxoBuf)

	addr, _ := bscript.NewAddressFromString(address)
	hash := addr.PublicKeyHash + "00"
	tapeSizeHex := bt.GetSizeHex(tapeSize)

	// FT mint code script 模板（与 JS 一致）
	codeTemplate := fmt.Sprintf(`OP_9 OP_PICK OP_TOALTSTACK OP_1 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_5 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x28 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_4 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x28 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_3 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x28 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_2 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x28 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_1 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x28 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_SWAP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_DUP OP_0 0x01 0x28 OP_MUL OP_SPLIT 0x01 0x28 OP_SPLIT OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_ENDIF OP_ADD OP_FROMALTSTACK OP_DROP OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_FROMALTSTACK OP_CAT OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_TOALTSTACK OP_3 OP_PICK OP_TOALTSTACK OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_TOALTSTACK OP_SHA256 OP_FROMALTSTACK OP_CAT OP_CAT OP_SHA256 OP_SHA256 OP_6 OP_PUSH_META 0x01 0x20 OP_SPLIT OP_DROP OP_EQUALVERIFY OP_FROMALTSTACK OP_1 OP_SPLIT OP_NIP 0x01 0x14 OP_SPLIT OP_1 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_IF OP_DROP OP_1 OP_PICK OP_HASH160 OP_EQUALVERIFY OP_CHECKSIGVERIFY OP_ELSE OP_1 OP_EQUALVERIFY OP_2 OP_PICK OP_HASH160 OP_EQUALVERIFY OP_TOALTSTACK OP_CAT OP_TOALTSTACK OP_DUP OP_0 OP_EQUALVERIFY OP_FROMALTSTACK OP_FROMALTSTACK OP_CAT OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_SHA256 OP_SHA256 OP_OVER 0x01 0x20 OP_SPLIT OP_4 OP_SPLIT OP_DROP OP_BIN2NUM OP_0 OP_EQUALVERIFY OP_EQUALVERIFY OP_SHA256 OP_5 OP_PUSH_META OP_EQUALVERIFY OP_CHECKSIGVERIFY OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_SHA256 OP_SHA256 OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_FROMALTSTACK OP_FROMALTSTACK 0x01 0x24 OP_SPLIT OP_DROP OP_DUP OP_TOALTSTACK 0x01 0x20 OP_SPLIT OP_DROP OP_5 OP_ROLL OP_EQUALVERIFY OP_2SWAP OP_EQUAL OP_IF OP_FROMALTSTACK OP_DROP OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_FROMALTSTACK 0x24 0x%s OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_SHA256 OP_SHA256 OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_FROMALTSTACK OP_FROMALTSTACK 0x01 0x20 OP_SPLIT OP_DROP OP_5 OP_ROLL OP_EQUALVERIFY OP_2SWAP OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_SHA256 OP_SHA256 OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_FROMALTSTACK OP_FROMALTSTACK 0x01 0x20 OP_SPLIT OP_DROP OP_5 OP_ROLL OP_EQUALVERIFY OP_2SWAP OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_SHA256 OP_SHA256 OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_FROMALTSTACK OP_FROMALTSTACK 0x01 0x20 OP_SPLIT OP_DROP OP_5 OP_ROLL OP_EQUALVERIFY OP_2SWAP OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_SHA256 OP_SHA256 OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_FROMALTSTACK OP_FROMALTSTACK 0x01 0x20 OP_SPLIT OP_DROP OP_5 OP_ROLL OP_EQUALVERIFY OP_2SWAP OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_2 OP_PICK OP_2 OP_PICK OP_CAT OP_TOALTSTACK OP_TOALTSTACK OP_PARTIAL_HASH OP_CAT OP_CAT OP_FROMALTSTACK OP_CAT OP_SHA256 OP_CAT OP_CAT OP_CAT OP_SHA256 OP_SHA256 OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_FROMALTSTACK OP_FROMALTSTACK 0x01 0x20 OP_SPLIT OP_DROP OP_5 OP_ROLL OP_EQUALVERIFY OP_2SWAP OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_0 OP_EQUALVERIFY OP_7 OP_EQUALVERIFY OP_FROMALTSTACK OP_FROMALTSTACK OP_SWAP OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_DUP OP_2 OP_EQUAL OP_IF OP_DROP OP_DUP OP_SIZE OP_DUP 0x01 0x%s OP_EQUAL OP_IF OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUALVERIFY OP_3 OP_SPLIT OP_SWAP OP_DROP OP_FROMALTSTACK OP_DUP OP_8 OP_MUL OP_2 OP_ROLL OP_SWAP OP_SPLIT OP_8 OP_SPLIT OP_DROP OP_BIN2NUM OP_DUP OP_0 OP_EQUAL OP_NOTIF OP_FROMALTSTACK OP_FROMALTSTACK OP_DUP OP_9 OP_PICK OP_9 OP_PICK OP_CAT OP_EQUALVERIFY OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_FROMALTSTACK OP_SWAP OP_SUB OP_TOALTSTACK OP_DROP OP_TOALTSTACK OP_SHA256 OP_CAT OP_TOALTSTACK OP_PARTIAL_HASH OP_FROMALTSTACK OP_CAT OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ELSE OP_DROP 0x01 0x%s OP_EQUAL OP_IF OP_2 OP_PICK OP_SIZE OP_5 OP_SUB OP_SPLIT 0x05 0x4654617065 OP_EQUAL OP_0 OP_EQUALVERIFY OP_DROP OP_ENDIF OP_PARTIAL_HASH OP_CAT OP_FROMALTSTACK OP_FROMALTSTACK OP_FROMALTSTACK OP_3 OP_ROLL OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_TOALTSTACK OP_ENDIF OP_ENDIF OP_DUP OP_2 OP_EQUAL OP_0 OP_EQUALVERIFY OP_FROMALTSTACK OP_FROMALTSTACK OP_0 OP_EQUALVERIFY OP_DROP OP_1 OP_EQUALVERIFY OP_FROMALTSTACK OP_FROMALTSTACK OP_SHA256 OP_7 OP_PUSH_META OP_EQUAL OP_NIP 0x17 0xffffffffffffffffffffffffffffffffffffffffffffff OP_DROP OP_RETURN 0x15 0x%s 0x05 0x32436f6465`,
		utxoHex, tapeSizeHex, tapeSizeHex, hash)

	s, err := bscript.NewFromASM(codeTemplate)
	if err != nil {
		panic(err)
	}
	return s
}

func newFeeQuote80() *bt.FeeQuote {
	fq := bt.NewFeeQuote()
	fq.AddQuote(bt.FeeTypeStandard, &bt.Fee{
		FeeType:   bt.FeeTypeStandard,
		MiningFee: bt.FeeUnit{Satoshis: 80, Bytes: 1000},
		RelayFee:  bt.FeeUnit{Satoshis: 80, Bytes: 1000},
	})
	fq.AddQuote(bt.FeeTypeData, &bt.Fee{
		FeeType:   bt.FeeTypeData,
		MiningFee: bt.FeeUnit{Satoshis: 80, Bytes: 1000},
		RelayFee:  bt.FeeUnit{Satoshis: 80, Bytes: 1000},
	})
	return fq
}

func hexDecode(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}
