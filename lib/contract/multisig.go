package contract

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/libsv/go-bk/base58"
	"github.com/libsv/go-bk/bec"
	"github.com/libsv/go-bk/crypto"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/go-bt/v2/sighash"
	"github.com/sCrypt-Inc/go-bt/v2/unlocker"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

const (
	ftV1ByteLen = 1564
	ftV2ByteLen = 1884
)

// MultiSigTxRawSendTBC 与 JS buildMultiSigTransaction_sendTBC 返回值一致
type MultiSigTxRawSendTBC struct {
	TxRaw   string
	Amounts []uint64
}

// MultiSigTxRawTransferFT 与 JS buildMultiSigTransaction_transferFT 返回值一致
type MultiSigTxRawTransferFT struct {
	TxRaw   string
	Amounts []uint64
}

// GetMultiSigAddress 与 MultiSig.getMultiSigAddress 一致
func GetMultiSigAddress(pubKeys []string, signatureCount, publicKeyCount int) (string, error) {
	if signatureCount < 1 || signatureCount > 6 {
		return "", fmt.Errorf("Invalid signatureCount.")
	}
	if publicKeyCount < 3 || publicKeyCount > 10 {
		return "", fmt.Errorf("Invalid publicKeyCount.")
	}
	if signatureCount > publicKeyCount {
		return "", fmt.Errorf("SignatureCount must be less than publicKeyCount.")
	}
	h, err := multiSigConcatHash(pubKeys)
	if err != nil {
		return "", err
	}
	prefix := byte((signatureCount << 4) | (publicKeyCount & 0x0f))
	payload := append([]byte{prefix}, h...)
	chk := crypto.Sha256d(payload)[:4]
	withCS := append(payload, chk...)
	return base58.Encode(withCS), nil
}

// GetSignatureAndPublicKeyCount 与 MultiSig.getSignatureAndPublicKeyCount 一致
func GetSignatureAndPublicKeyCount(address string) (signatureCount, publicKeyCount int, err error) {
	buf := base58.Decode(address)
	if len(buf) < 5 {
		return 0, 0, fmt.Errorf("invalid address")
	}
	prefix := buf[0]
	signatureCount = int((prefix >> 4) & 0x0f)
	publicKeyCount = int(prefix & 0x0f)
	return signatureCount, publicKeyCount, nil
}

// VerifyMultiSigAddress 与 MultiSig.verifyMultiSigAddress 一致
func VerifyMultiSigAddress(pubKeys []string, address string) (bool, error) {
	hFromKeys, err := multiSigConcatHash(pubKeys)
	if err != nil {
		return false, err
	}
	buf := base58.Decode(address)
	if len(buf) < 21 {
		return false, nil
	}
	hFromAddr := buf[1:21]
	return hex.EncodeToString(hFromKeys) == hex.EncodeToString(hFromAddr), nil
}

// ValidateMultiSigAddress 与 MultiSig.validateMultiSigAddress 一致
func ValidateMultiSigAddress(address string) bool {
	if len(address) != 33 && len(address) != 34 {
		return false
	}
	buf := base58.Decode(address)
	if len(buf) < 25 {
		return false
	}
	prefix := buf[0]
	sig := int((prefix >> 4) & 0x0f)
	pkc := int(prefix & 0x0f)
	if sig < 1 || sig > 6 || pkc < 3 || pkc > 10 || sig > pkc {
		return false
	}
	payload := buf[:len(buf)-4]
	chk := buf[len(buf)-4:]
	sum := crypto.Sha256d(payload)[:4]
	return bytes.Equal(chk, sum)
}

// GetMultiSigLockScript 与 MultiSig.getMultiSigLockScript 一致（返回 script_asm 字符串）
func GetMultiSigLockScript(address string) (string, error) {
	buf := base58.Decode(address)
	if len(buf) < 21 {
		return "", fmt.Errorf("invalid address")
	}
	signatureCount, publicKeyCount, err := GetSignatureAndPublicKeyCount(address)
	if err != nil {
		return "", err
	}
	if signatureCount < 1 || signatureCount > 6 {
		return "", fmt.Errorf("Invalid signatureCount.")
	}
	if publicKeyCount < 3 || publicKeyCount > 10 {
		return "", fmt.Errorf("Invalid publicKeyCount.")
	}
	if signatureCount > publicKeyCount {
		return "", fmt.Errorf("SignatureCount must be less than publicKeyCount.")
	}
	hash := hex.EncodeToString(buf[1:21])
	var lockPrefix string
	for i := 0; i < publicKeyCount-1; i++ {
		lockPrefix += "21 OP_SPLIT "
	}
	for i := 0; i < publicKeyCount; i++ {
		lockPrefix += fmt.Sprintf("OP_%d OP_PICK ", publicKeyCount-1)
	}
	for i := 0; i < publicKeyCount-1; i++ {
		lockPrefix += "OP_CAT "
	}
	asm := fmt.Sprintf("OP_%d OP_SWAP %sOP_HASH160 %s OP_EQUALVERIFY OP_%d OP_CHECKMULTISIG",
		signatureCount, lockPrefix, hash, publicKeyCount)
	return asm, nil
}

// GetCombineHash 与 MultiSig.getCombineHash 一致
func GetCombineHash(address string) (string, error) {
	asm, err := GetMultiSigLockScript(address)
	if err != nil {
		return "", err
	}
	s, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	h := crypto.Hash160(crypto.Sha256(s.Bytes()))
	return hex.EncodeToString(h) + "01", nil
}

// BuildHoldScript 与 MultiSig.buildHoldScript 一致
func BuildHoldScript(pubKeyHex string) (*bscript.Script, error) {
	pk, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		return nil, err
	}
	h := crypto.Hash160(pk)
	return bscript.NewFromASM(fmt.Sprintf("OP_DUP OP_HASH160 0x14 0x%s OP_EQUALVERIFY OP_CHECKSIG OP_RETURN 0x08 0x6d756c7469736967", hex.EncodeToString(h)))
}

// BuildTapeScript 与 MultiSig.buildTapeScript 一致
func BuildTapeScript(address string, pubKeys []string) (*bscript.Script, error) {
	// 与 JS JSON.stringify 键顺序一致：address, pubkeys
	data := fmt.Sprintf(`{"address":"%s","pubkeys":[`, address)
	for i, pk := range pubKeys {
		if i > 0 {
			data += ","
		}
		data += `"` + pk + `"`
	}
	data += `]}`
	dataHex := hex.EncodeToString([]byte(data))
	return bscript.NewFromASM("OP_FALSE OP_RETURN " + dataHex + " 4d54617065")
}

// CreateMultiSigWallet 与 MultiSig.createMultiSigWallet 一致
func CreateMultiSigWallet(addressFrom string, pubKeys []string, signatureCount, publicKeyCount int, tbcAmount float64, utxos []*bt.UTXO, privKey *bec.PrivateKey) (string, error) {
	msAddr, err := GetMultiSigAddress(pubKeys, signatureCount, publicKeyCount)
	if err != nil {
		return "", err
	}
	asm, err := GetMultiSigLockScript(msAddr)
	if err != nil {
		return "", err
	}
	lockScript, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	amt := util.ParseDecimalToBigInt(fmt.Sprintf("%g", tbcAmount), 6)
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: lockScript, Satoshis: amt.Uint64()})
	for _, pk := range pubKeys {
		hs, err := BuildHoldScript(pk)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: hs, Satoshis: 200})
	}
	ts, err := BuildTapeScript(msAddr, pubKeys)
	if err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: ts, Satoshis: 0})
	if err := applyMultiSigFeeAndChange(tx, addressFrom); err != nil {
		return "", err
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// P2PKHToMultiSigSendTBC 与 MultiSig.p2pkhToMultiSig_sendTBC 一致
func P2PKHToMultiSigSendTBC(addressFrom, addressTo string, tbcAmount float64, utxos []*bt.UTXO, privKey *bec.PrivateKey) (string, error) {
	asm, err := GetMultiSigLockScript(addressTo)
	if err != nil {
		return "", err
	}
	lockScript, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	amt := util.ParseDecimalToBigInt(fmt.Sprintf("%g", tbcAmount), 6)
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: lockScript, Satoshis: amt.Uint64()})
	if err := applyMultiSigFeeAndChange(tx, addressFrom); err != nil {
		return "", err
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// BuildMultiSigTransactionSendTBC 与 MultiSig.buildMultiSigTransaction_sendTBC 一致
func BuildMultiSigTransactionSendTBC(addressFrom, addressTo string, tbcAmount float64, utxos []*bt.UTXO) (*MultiSigTxRawSendTBC, error) {
	asmFrom, err := GetMultiSigLockScript(addressFrom)
	if err != nil {
		return nil, err
	}
	lockFrom, err := bscript.NewFromASM(asmFrom)
	if err != nil {
		return nil, err
	}
	amt := util.ParseDecimalToBigInt(fmt.Sprintf("%g", tbcAmount), 6)
	var count uint64
	amounts := make([]uint64, len(utxos))
	for i, u := range utxos {
		count += u.Satoshis
		amounts[i] = u.Satoshis
	}
	fee := uint64((len(utxos) + 9) / 10 * 1000)
	if count < amt.Uint64()+fee {
		return nil, fmt.Errorf("insufficient inputs")
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return nil, err
	}
	tx.AddOutput(&bt.Output{LockingScript: lockFrom, Satoshis: count - amt.Uint64() - fee})
	if ok, _ := bscript.ValidateAddress(addressTo); ok {
		addr, err := bscript.NewAddressFromString(addressTo)
		if err != nil {
			return nil, err
		}
		ls, err := bscript.NewP2PKHFromPubKeyHash(hexDecode(addr.PublicKeyHash))
		if err != nil {
			return nil, err
		}
		tx.AddOutput(&bt.Output{LockingScript: ls, Satoshis: amt.Uint64()})
	} else {
		asmTo, err := GetMultiSigLockScript(addressTo)
		if err != nil {
			return nil, err
		}
		ls, err := bscript.NewFromASM(asmTo)
		if err != nil {
			return nil, err
		}
		tx.AddOutput(&bt.Output{LockingScript: ls, Satoshis: amt.Uint64()})
	}
	return &MultiSigTxRawSendTBC{TxRaw: hex.EncodeToString(tx.Bytes()), Amounts: amounts}, nil
}

// SignMultiSigTransactionSendTBC 与 MultiSig.signMultiSigTransaction_sendTBC 一致
func SignMultiSigTransactionSendTBC(multiSigAddress string, raw *MultiSigTxRawSendTBC, privKey *bec.PrivateKey) ([]string, error) {
	asm, err := GetMultiSigLockScript(multiSigAddress)
	if err != nil {
		return nil, err
	}
	lockScript, err := bscript.NewFromASM(asm)
	if err != nil {
		return nil, err
	}
	tx, err := bt.NewTxFromString(raw.TxRaw)
	if err != nil {
		return nil, err
	}
	tx.Version = 10
	for i := range tx.Inputs {
		tx.Inputs[i].PreviousTxScript = lockScript
		tx.Inputs[i].PreviousTxSatoshis = raw.Amounts[i]
	}
	sigs := make([]string, len(tx.Inputs))
	for i := range tx.Inputs {
		sh, err := tx.CalcInputSignatureHash(uint32(i), sighash.AllForkID)
		if err != nil {
			return nil, err
		}
		sig, err := privKey.Sign(sh)
		if err != nil {
			return nil, err
		}
		b := append(sig.Serialise(), byte(sighash.AllForkID))
		sigs[i] = hex.EncodeToString(b)
	}
	return sigs, nil
}

// BatchSignMultiSigTransactionSendTBC 与 MultiSig.batchSignMultiSigTransaction_sendTBC 一致
func BatchSignMultiSigTransactionSendTBC(multiSigAddress string, raws []*MultiSigTxRawSendTBC, privKey *bec.PrivateKey) ([][]string, error) {
	out := make([][]string, len(raws))
	for i, r := range raws {
		s, err := SignMultiSigTransactionSendTBC(multiSigAddress, r, privKey)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

// FinishMultiSigTransactionSendTBC 与 MultiSig.finishMultiSigTransaction_sendTBC 一致
func FinishMultiSigTransactionSendTBC(txraw string, sigs [][]string, pubKeys []string) (string, error) {
	tx, err := bt.NewTxFromString(txraw)
	if err != nil {
		return "", err
	}
	tx.Version = 10
	multiPub := strings.Join(pubKeys, "")
	for j := range tx.Inputs {
		var sigLine string
		for i, s := range sigs[j] {
			if i > 0 {
				sigLine += " "
			}
			sigLine += s
		}
		asm := fmt.Sprintf("OP_0 %s %s", sigLine, multiPub)
		us, err := bscript.NewFromASM(asm)
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(j), us); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// BatchFinishMultiSigTransactionSendTBC 与 MultiSig.batchFinishMultiSigTransaction_sendTBC 一致
func BatchFinishMultiSigTransactionSendTBC(txraws []string, sigs [][][]string, pubKeys []string) ([]string, error) {
	out := make([]string, len(txraws))
	for i := range txraws {
		f, err := FinishMultiSigTransactionSendTBC(txraws[i], sigs[i], pubKeys)
		if err != nil {
			return nil, err
		}
		out[i] = f
	}
	return out, nil
}

// P2PKHToMultiSigTransferFT 与 MultiSig.p2pkhToMultiSig_transferFT 一致
func P2PKHToMultiSigTransferFT(
	addressFrom, addressTo string,
	ft *FT,
	ftAmount string,
	utxo *bt.UTXO,
	ftutxos []*bt.FtUTXO,
	preTX []*bt.Tx,
	prepreTxData []string,
	privKey *bec.PrivateKey,
	tbcAmount *float64,
) (string, error) {
	code := ft.CodeScript
	tape := ft.TapeScript
	dec := ft.Decimal
	if strings.TrimSpace(ftAmount) != "" {
		if f, err := strconv.ParseFloat(ftAmount, 64); err == nil && f < 0 {
			return "", fmt.Errorf("Invalid amount")
		}
	}
	amountbn := util.ParseDecimalToBigInt(ftAmount, dec)
	tapeAmountSet := make([]*big.Int, len(ftutxos))
	tapeSum := big.NewInt(0)
	for i, u := range ftutxos {
		b, ok := new(big.Int).SetString(u.FtBalance, 10)
		if !ok {
			return "", fmt.Errorf("invalid ft balance")
		}
		tapeAmountSet[i] = b
		tapeSum.Add(tapeSum, b)
	}
	if amountbn.Cmp(tapeSum) > 0 {
		return "", fmt.Errorf("Insufficient balance, please add more FT UTXOs")
	}
	if dec > 18 {
		return "", fmt.Errorf("The maximum value for decimal cannot exceed 18")
	}
	maxAmt := util.ParseDecimalToBigInt("1", 18-dec)
	if amountbn.Cmp(maxAmt) > 0 {
		return "", fmt.Errorf("When decimal is %d, the maximum amount cannot exceed %s", dec, maxAmt.String())
	}
	amountHex, changeHex := BuildTapeAmount(amountbn, tapeAmountSet)
	asm, err := GetMultiSigLockScript(addressTo)
	if err != nil {
		return "", err
	}
	lockTo, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	tx := newFTTx()
	ftIns, err := util.FtUTXOsToUTXOs(ftutxos)
	if err != nil {
		return "", err
	}
	// 与 JS：from(ftutxos).from(utxo)，FT 输入在前，手续费 UTXO 在后
	if err := tx.FromUTXOs(ftIns...); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(utxo); err != nil {
		return "", err
	}
	if tbcAmount != nil {
		sat := util.ParseDecimalToBigInt(fmt.Sprintf("%g", *tbcAmount), 6)
		tx.AddOutput(&bt.Output{LockingScript: lockTo, Satoshis: sat.Uint64()})
	}
	hash := crypto.Hash160(crypto.Sha256(lockTo.Bytes()))
	codeScript := BuildFTtransferCode(code, hex.EncodeToString(hash))
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 2000})
	tapeScript := BuildFTtransferTape(tape, amountHex)
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})
	if amountbn.Cmp(tapeSum) < 0 {
		changeCode := BuildFTtransferCode(code, addressFrom)
		tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: 2000})
		changeTape := BuildFTtransferTape(tape, changeHex)
		tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
	}
	if err := applyMultiSigFeeAndChange(tx, addressFrom); err != nil {
		return "", err
	}
	ftUnlocks := make([]*bscript.Script, len(ftutxos))
	for i := range ftutxos {
		us, err := ft.getFTunlock(privKey, tx, preTX[i], prepreTxData[i], i, int(ftutxos[i].Vout))
		if err != nil {
			return "", err
		}
		ftUnlocks[i] = us
	}
	ctx := context.Background()
	ug := newFTTransferUnlockerGetter(ftUnlocks, privKey)
	if err := tx.FillAllInputs(ctx, ug); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// BuildMultiSigTransactionTransferFT 与 MultiSig.buildMultiSigTransaction_transferFT 一致
func BuildMultiSigTransactionTransferFT(
	addressFrom, addressTo string,
	ft *FT,
	ftAmount string,
	utxo *bt.UTXO,
	ftutxos []*bt.FtUTXO,
	preTX []*bt.Tx,
	prepreTxData []string,
	contractTX *bt.Tx,
	privKey *bec.PrivateKey,
) (*MultiSigTxRawTransferFT, error) {
	code := ft.CodeScript
	tape := ft.TapeScript
	dec := ft.Decimal
	asmFrom, err := GetMultiSigLockScript(addressFrom)
	if err != nil {
		return nil, err
	}
	lockFrom, err := bscript.NewFromASM(asmFrom)
	if err != nil {
		return nil, err
	}
	hashFrom := crypto.Hash160(crypto.Sha256(lockFrom.Bytes()))
	amountbn := util.ParseDecimalToBigInt(ftAmount, dec)
	tapeAmountSet := make([]*big.Int, len(ftutxos))
	tapeSum := big.NewInt(0)
	for i, u := range ftutxos {
		b, ok := new(big.Int).SetString(u.FtBalance, 10)
		if !ok {
			return nil, fmt.Errorf("invalid ft balance")
		}
		tapeAmountSet[i] = b
		tapeSum.Add(tapeSum, b)
	}
	if amountbn.Cmp(tapeSum) > 0 {
		return nil, fmt.Errorf("Insufficient balance, please add more FT UTXOs")
	}
	if dec > 18 {
		return nil, fmt.Errorf("The maximum value for decimal cannot exceed 18")
	}
	maxAmt := util.ParseDecimalToBigInt("1", 18-dec)
	if amountbn.Cmp(maxAmt) > 0 {
		return nil, fmt.Errorf("When decimal is %d, the maximum amount cannot exceed %s", dec, maxAmt.String())
	}
	amountHex, changeHex := BuildTapeAmountWithFtInputIndex(amountbn, tapeAmountSet, 1)
	tx := newFTTx()
	if err := tx.FromUTXOs(utxo); err != nil {
		return nil, err
	}
	ftIns, err := util.FtUTXOsToUTXOs(ftutxos)
	if err != nil {
		return nil, err
	}
	if err := tx.FromUTXOs(ftIns...); err != nil {
		return nil, err
	}
	var satOut uint64
	switch len(ftutxos) {
	case 1:
		satOut = utxo.Satoshis - 4000
	case 2:
		satOut = utxo.Satoshis - 5500
	case 3:
		satOut = utxo.Satoshis - 7000
	case 4:
		satOut = utxo.Satoshis - 8500
	case 5:
		satOut = utxo.Satoshis - 10000
	default:
		return nil, fmt.Errorf("unsupported ft input count %d", len(ftutxos))
	}
	tx.AddOutput(&bt.Output{LockingScript: lockFrom, Satoshis: satOut})
	var codeOut *bscript.Script
	if strings.HasPrefix(addressTo, "1") {
		codeOut = BuildFTtransferCode(code, addressTo)
	} else {
		asmTo, err := GetMultiSigLockScript(addressTo)
		if err != nil {
			return nil, err
		}
		ls, err := bscript.NewFromASM(asmTo)
		if err != nil {
			return nil, err
		}
		hTo := crypto.Hash160(crypto.Sha256(ls.Bytes()))
		codeOut = BuildFTtransferCode(code, hex.EncodeToString(hTo))
	}
	tx.AddOutput(&bt.Output{LockingScript: codeOut, Satoshis: 2000})
	tx.AddOutput(&bt.Output{LockingScript: BuildFTtransferTape(tape, amountHex), Satoshis: 0})
	if amountbn.Cmp(tapeSum) < 0 {
		tx.AddOutput(&bt.Output{LockingScript: BuildFTtransferCode(code, hex.EncodeToString(hashFrom)), Satoshis: 2000})
		tx.AddOutput(&bt.Output{LockingScript: BuildFTtransferTape(tape, changeHex), Satoshis: 0})
	}
	ftVer := 1
	if len(ftutxos) > 0 && len(ftutxos[0].Script)/2 == ftV2ByteLen {
		ftVer = 2
	}
	for i := range ftutxos {
		us, err := ft.getFTunlockSwap(privKey, tx, preTX[i], prepreTxData[i], contractTX, i+1, int(ftutxos[i].Vout), ftVer)
		if err != nil {
			return nil, err
		}
		if err := tx.InsertInputUnlockingScript(uint32(i+1), us); err != nil {
			return nil, err
		}
	}
	return &MultiSigTxRawTransferFT{TxRaw: hex.EncodeToString(tx.Bytes()), Amounts: []uint64{utxo.Satoshis}}, nil
}

// SignMultiSigTransactionTransferFT 与 MultiSig.signMultiSigTransaction_transferFT 一致
func SignMultiSigTransactionTransferFT(multiSigAddress string, raw *MultiSigTxRawTransferFT, privKey *bec.PrivateKey) ([]string, error) {
	asm, err := GetMultiSigLockScript(multiSigAddress)
	if err != nil {
		return nil, err
	}
	lockScript, err := bscript.NewFromASM(asm)
	if err != nil {
		return nil, err
	}
	tx, err := bt.NewTxFromString(raw.TxRaw)
	if err != nil {
		return nil, err
	}
	tx.Version = 10
	tx.Inputs[0].PreviousTxScript = lockScript
	tx.Inputs[0].PreviousTxSatoshis = raw.Amounts[0]
	sh, err := tx.CalcInputSignatureHash(0, sighash.AllForkID)
	if err != nil {
		return nil, err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return nil, err
	}
	b := append(sig.Serialise(), byte(sighash.AllForkID))
	return []string{hex.EncodeToString(b)}, nil
}

// BatchSignMultiSigTransactionTransferFT 与 MultiSig.batchSignMultiSigTransaction_transferFT 一致
func BatchSignMultiSigTransactionTransferFT(multiSigAddress string, raws []*MultiSigTxRawTransferFT, privKey *bec.PrivateKey) ([][]string, error) {
	out := make([][]string, len(raws))
	for i, r := range raws {
		s, err := SignMultiSigTransactionTransferFT(multiSigAddress, r, privKey)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

// FinishMultiSigTransactionTransferFT 与 MultiSig.finishMultiSigTransaction_transferFT 一致
func FinishMultiSigTransactionTransferFT(txraw string, sigs [][]string, pubKeys []string) (string, error) {
	tx, err := bt.NewTxFromString(txraw)
	if err != nil {
		return "", err
	}
	tx.Version = 10
	multiPub := strings.Join(pubKeys, "")
	var sigLine string
	for i, s := range sigs[0] {
		if i > 0 {
			sigLine += " "
		}
		sigLine += s
	}
	asm := fmt.Sprintf("OP_0 %s %s", sigLine, multiPub)
	us, err := bscript.NewFromASM(asm)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, us); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// BatchFinishMultiSigTransactionTransferFT 与 MultiSig.batchFinishMultiSigTransaction_transferFT 一致
func BatchFinishMultiSigTransactionTransferFT(txraws []string, sigs [][][]string, pubKeys []string) ([]string, error) {
	out := make([]string, len(txraws))
	for i := range txraws {
		f, err := FinishMultiSigTransactionTransferFT(txraws[i], sigs[i], pubKeys)
		if err != nil {
			return nil, err
		}
		out[i] = f
	}
	return out, nil
}

func multiSigConcatHash(pubKeys []string) ([]byte, error) {
	var buf []byte
	for _, pk := range pubKeys {
		b, err := hex.DecodeString(pk)
		if err != nil {
			return nil, err
		}
		buf = append(buf, b...)
	}
	return crypto.Hash160(buf), nil
}

func applyMultiSigFeeAndChange(tx *bt.Tx, changeAddr string) error {
	return tx.ChangeToAddress(changeAddr, newFeeQuote80())
}
