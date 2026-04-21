package util

// 与 tbc-contract/lib/util/nftunlock.ts 对齐（NFT 转移 / coinNft 解锁用 txdata，非 FT 的 ftunlock）。

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/libsv/go-bk/crypto"
)

const nftUnlockTxVersion = 10

var (
	nftVlioLen    = []byte{0x10}
	nftAmountLen  = []byte{0x08}
	nftHashLen    = []byte{0x20}
)

// NftGetLengthHex 对齐 getLengthHex（仅表示「按长度做 push 前缀」的裸前缀字节；一般应优先用 NftEncodeMinimalPushData(data)）。
func NftGetLengthHex(length int) []byte {
	if length < 76 {
		return []byte{byte(length)}
	}
	if length <= 255 {
		return append([]byte{0x4c}, byte(length))
	}
	if length <= 65535 {
		b := make([]byte, 2)
		binary.LittleEndian.PutUint16(b, uint16(length))
		return append([]byte{0x4d}, b...)
	}
	if length <= 0xFFFFFFFF {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(length))
		return append([]byte{0x4e}, b...)
	}
	panic("length exceeds maximum supported size (4 GB)")
}

// NftEncodeMinimalPushData 将 data 编码为单个 push，满足 SCRIPT_VERIFY_MINIMALDATA（与 go-bt interpreter opcodeparser.enforceMinimumDataPush 一致）。
// 单字节且值为 1..16 时须用 OP_1..OP_16，不得使用 OP_PUSHBYTES_1，否则链上报 mandatory-script-verify-flag-failed (Data push larger than necessary)。
func NftEncodeMinimalPushData(data []byte) []byte {
	if len(data) == 0 {
		return []byte{0x00}
	}
	if len(data) == 1 {
		b := data[0]
		if b >= 1 && b <= 16 {
			return []byte{0x51 + b - 1}
		}
		if b == 0x81 {
			return []byte{0x4f}
		}
	}
	p, err := bscript.PushDataPrefix(data)
	if err != nil {
		panic(err)
	}
	return append(p, data...)
}

func nftAppendOutputsData(tx *bt.Tx, fromIdx int) []byte {
	var w bytes.Buffer
	for i := fromIdx; i < len(tx.Outputs); i++ {
		o := tx.Outputs[i]
		sat := make([]byte, 8)
		binary.LittleEndian.PutUint64(sat, o.Satoshis)
		w.Write(sat)
		w.Write(crypto.Sha256(o.LockingScript.Bytes()))
	}
	raw := w.Bytes()
	return NftEncodeMinimalPushData(raw)
}

// GetNFTCurrentTxdata 对齐 getCurrentTxdata。
func GetNFTCurrentTxdata(tx *bt.Tx) (string, error) {
	if len(tx.Outputs) < 1 {
		return "", fmt.Errorf("outputs missing for GetNFTCurrentTxdata")
	}
	o0 := tx.Outputs[0]
	var w bytes.Buffer
	w.Write(nftAmountLen)
	sat0 := make([]byte, 8)
	binary.LittleEndian.PutUint64(sat0, o0.Satoshis)
	w.Write(sat0)
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(o0.LockingScript.Bytes()))
	w.Write(nftAppendOutputsData(tx, 1))
	return hex.EncodeToString(w.Bytes()), nil
}

// GetNFTPreTxdata 对齐 getPreTxdata（不含 vout 参数，整笔 pre 交易）。
func GetNFTPreTxdata(tx *bt.Tx) (string, error) {
	if len(tx.Outputs) < 2 {
		return "", fmt.Errorf("outputs missing for GetNFTPreTxdata")
	}
	var w bytes.Buffer
	w.Write(nftVlioLen)
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, nftUnlockTxVersion)
	w.Write(b)
	binary.LittleEndian.PutUint32(b, tx.LockTime)
	w.Write(b)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Inputs)))
	w.Write(b)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Outputs)))
	w.Write(b)

	var in1, in2 bytes.Buffer
	for _, in := range tx.Inputs {
		prevID := bt.ReverseBytes(in.PreviousTxID())
		in1.Write(prevID)
		oi := make([]byte, 4)
		binary.LittleEndian.PutUint32(oi, in.PreviousTxOutIndex)
		in1.Write(oi)
		binary.LittleEndian.PutUint32(oi, in.SequenceNumber)
		in1.Write(oi)
		h := crypto.Sha256(in.UnlockingScript.Bytes())
		in2.Write(h)
	}
	w.Write(NftEncodeMinimalPushData(in1.Bytes()))
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(in2.Bytes()))

	o0 := tx.Outputs[0]
	w.Write(nftAmountLen)
	sat := make([]byte, 8)
	binary.LittleEndian.PutUint64(sat, o0.Satoshis)
	w.Write(sat)
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(o0.LockingScript.Bytes()))

	o1 := tx.Outputs[1]
	w.Write(nftAmountLen)
	binary.LittleEndian.PutUint64(sat, o1.Satoshis)
	w.Write(sat)
	w.Write(NftEncodeMinimalPushData(o1.LockingScript.Bytes()))
	w.Write(nftAppendOutputsData(tx, 2))
	return hex.EncodeToString(w.Bytes()), nil
}

// GetNFTPrePreTxdata 对齐 getPrePreTxdata。
func GetNFTPrePreTxdata(tx *bt.Tx) (string, error) {
	if len(tx.Outputs) < 1 {
		return "", fmt.Errorf("outputs missing for GetNFTPrePreTxdata")
	}
	var w bytes.Buffer
	w.Write(nftVlioLen)
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, nftUnlockTxVersion)
	w.Write(b)
	binary.LittleEndian.PutUint32(b, tx.LockTime)
	w.Write(b)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Inputs)))
	w.Write(b)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Outputs)))
	w.Write(b)

	var in1, in2 bytes.Buffer
	for _, in := range tx.Inputs {
		prevID := bt.ReverseBytes(in.PreviousTxID())
		in1.Write(prevID)
		oi := make([]byte, 4)
		binary.LittleEndian.PutUint32(oi, in.PreviousTxOutIndex)
		in1.Write(oi)
		binary.LittleEndian.PutUint32(oi, in.SequenceNumber)
		in1.Write(oi)
		h := crypto.Sha256(in.UnlockingScript.Bytes())
		in2.Write(h)
	}
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(in1.Bytes()))
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(in2.Bytes()))

	o0 := tx.Outputs[0]
	w.Write(nftAmountLen)
	sat := make([]byte, 8)
	binary.LittleEndian.PutUint64(sat, o0.Satoshis)
	w.Write(sat)
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(o0.LockingScript.Bytes()))
	w.Write(nftAppendOutputsData(tx, 1))
	return hex.EncodeToString(w.Bytes()), nil
}

// GetNFTCurrentTxdataV0 对齐 NFT.getCurrentTxdata_v0（output[0] 为完整 script + 变长前缀，非 hash）。
func GetNFTCurrentTxdataV0(tx *bt.Tx) (string, error) {
	if len(tx.Outputs) < 1 {
		return "", fmt.Errorf("outputs missing for GetNFTCurrentTxdataV0")
	}
	o0 := tx.Outputs[0]
	var w bytes.Buffer
	w.Write(nftAmountLen)
	sat0 := make([]byte, 8)
	binary.LittleEndian.PutUint64(sat0, o0.Satoshis)
	w.Write(sat0)
	scr := o0.LockingScript.Bytes()
	w.Write(NftEncodeMinimalPushData(scr))
	w.Write(nftAppendOutputsData(tx, 1))
	return hex.EncodeToString(w.Bytes()), nil
}

// GetNFTPreTxdataV0 对齐 NFT.getPreTxdata_v0。
func GetNFTPreTxdataV0(tx *bt.Tx) (string, error) {
	if len(tx.Outputs) < 2 {
		return "", fmt.Errorf("outputs missing for GetNFTPreTxdataV0")
	}
	var w bytes.Buffer
	w.Write(nftVlioLen)
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, nftUnlockTxVersion)
	w.Write(b)
	binary.LittleEndian.PutUint32(b, tx.LockTime)
	w.Write(b)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Inputs)))
	w.Write(b)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Outputs)))
	w.Write(b)

	var in1, in2 bytes.Buffer
	for _, in := range tx.Inputs {
		prevID := bt.ReverseBytes(in.PreviousTxID())
		in1.Write(prevID)
		oi := make([]byte, 4)
		binary.LittleEndian.PutUint32(oi, in.PreviousTxOutIndex)
		in1.Write(oi)
		binary.LittleEndian.PutUint32(oi, in.SequenceNumber)
		in1.Write(oi)
		h := crypto.Sha256(in.UnlockingScript.Bytes())
		in2.Write(h)
	}
	w.Write(NftEncodeMinimalPushData(in1.Bytes()))
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(in2.Bytes()))

	writeOutFull := func(idx int) {
		o := tx.Outputs[idx]
		w.Write(nftAmountLen)
		sat := make([]byte, 8)
		binary.LittleEndian.PutUint64(sat, o.Satoshis)
		w.Write(sat)
		ls := o.LockingScript.Bytes()
		w.Write(NftEncodeMinimalPushData(ls))
	}
	writeOutFull(0)
	writeOutFull(1)
	w.Write(nftAppendOutputsData(tx, 2))
	return hex.EncodeToString(w.Bytes()), nil
}

// GetNFTPrePreTxdataV0 对齐 NFT.getPrePreTxdata_v0。
func GetNFTPrePreTxdataV0(tx *bt.Tx) (string, error) {
	if len(tx.Outputs) < 1 {
		return "", fmt.Errorf("outputs missing for GetNFTPrePreTxdataV0")
	}
	var w bytes.Buffer
	w.Write(nftVlioLen)
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, nftUnlockTxVersion)
	w.Write(b)
	binary.LittleEndian.PutUint32(b, tx.LockTime)
	w.Write(b)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Inputs)))
	w.Write(b)
	binary.LittleEndian.PutUint32(b, uint32(len(tx.Outputs)))
	w.Write(b)

	var in1, in2 bytes.Buffer
	for _, in := range tx.Inputs {
		prevID := bt.ReverseBytes(in.PreviousTxID())
		in1.Write(prevID)
		oi := make([]byte, 4)
		binary.LittleEndian.PutUint32(oi, in.PreviousTxOutIndex)
		in1.Write(oi)
		binary.LittleEndian.PutUint32(oi, in.SequenceNumber)
		in1.Write(oi)
		h := crypto.Sha256(in.UnlockingScript.Bytes())
		in2.Write(h)
	}
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(in1.Bytes()))
	w.Write(nftHashLen)
	w.Write(crypto.Sha256(in2.Bytes()))

	o0 := tx.Outputs[0]
	w.Write(nftAmountLen)
	sat := make([]byte, 8)
	binary.LittleEndian.PutUint64(sat, o0.Satoshis)
	w.Write(sat)
	ls := o0.LockingScript.Bytes()
	w.Write(NftEncodeMinimalPushData(ls))
	w.Write(nftAppendOutputsData(tx, 1))
	return hex.EncodeToString(w.Bytes()), nil
}

// GetTapePushSize 对齐 ftunlock.getSize（用于 stableCoin tape 等脚本中的长度 push）。
func GetTapePushSize(length int) []byte {
	if length < 256 {
		return []byte{byte(length)}
	}
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(length))
	return b
}
