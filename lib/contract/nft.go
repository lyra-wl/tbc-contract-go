// Package contract — NFT 合约（对齐 tbc-contract/lib/contract/nft.ts，不含旧版 poolNFT）。
package contract

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/libsv/go-bk/bec"
	"github.com/libsv/go-bk/crypto"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/go-bt/v2/sighash"
	"github.com/sCrypt-Inc/go-bt/v2/unlocker"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

//go:embed nft_code.asm
var nftCodeTemplateASM string

//go:embed nft_code_v0.asm
var nftCodeV0TemplateASM string

// NFTInfo 链上/初始化元数据（对齐 nft.ts NFTInfo）。
type NFTInfo struct {
	CollectionID         string
	CollectionIndex      int
	CollectionName       string
	NftName              string
	NftSymbol            string
	NftAttributes        string
	NftDescription       string
	NftTransferTimeCount int
	NftIcon              string
}

// CollectionData createCollection 参数（JSON 与 tbc-contract/lib/contract/nft.ts 中 CollectionData 一致：collectionName / description / supply / file）。
type CollectionData struct {
	CollectionName string `json:"collectionName"`
	Description    string `json:"description"`
	Supply         int    `json:"supply"`
	File           string `json:"file"`
}

// NFTData createNFT / tape 内容。
type NFTData struct {
	NftName     string
	Symbol      string
	Description string
	Attributes  string
	File        string
}

// NFT 非同质化代币句柄。
type NFT struct {
	CollectionID    string
	CollectionIndex int
	CollectionName  string
	TransferCount   int
	ContractID      string
	NftData         NFTData
}

// NewNFT 使用合约 id（通常为 collection 创世 txid）构造。
func NewNFT(contractID string) *NFT {
	return &NFT{ContractID: contractID}
}

// Initialize 用 NFTInfo 填充字段。
func (n *NFT) Initialize(info *NFTInfo) {
	file := n.ContractID + "00000000"
	n.NftData = NFTData{
		NftName:     info.NftName,
		Symbol:      info.NftSymbol,
		Description: info.NftDescription,
		Attributes:  info.NftAttributes,
		File:        file,
	}
	n.CollectionID = info.CollectionID
	n.CollectionIndex = info.CollectionIndex
	n.CollectionName = info.CollectionName
	n.TransferCount = info.NftTransferTimeCount
}

func nftUtxoHex(txHash string, vout uint32) (string, error) {
	txidBytes, err := hex.DecodeString(txHash)
	if err != nil || len(txidBytes) != 32 {
		return "", fmt.Errorf("invalid txid")
	}
	buf := make([]byte, 36)
	for i := 0; i < 32; i++ {
		buf[i] = txidBytes[31-i]
	}
	binary.LittleEndian.PutUint32(buf[32:], vout)
	return hex.EncodeToString(buf), nil
}

func parseNFTCodeASM(asm string) (*bscript.Script, error) {
	asm = collapseTbcMintASM(asm)
	asm = strip0xHexPushesInASM(asm)
	return bscript.NewFromASM(asm)
}

// BuildCodeScript 对齐 NFT.buildCodeScript（collection_id 为 32 字节 hex txid，index 为铸造时 output index）。
func BuildCodeScript(txHash string, outputIndex uint32) (*bscript.Script, error) {
	utxoHex, err := nftUtxoHex(txHash, outputIndex)
	if err != nil {
		return nil, err
	}
	asm := strings.ReplaceAll(nftCodeTemplateASM, "${utxoHex}", utxoHex)
	return parseNFTCodeASM(asm)
}

// BuildCodeScriptV0 对齐 NFT.buildCodeScript_v0。
func BuildCodeScriptV0(txHash string, outputIndex uint32) (*bscript.Script, error) {
	utxoHex, err := nftUtxoHex(txHash, outputIndex)
	if err != nil {
		return nil, err
	}
	asm := strings.ReplaceAll(nftCodeV0TemplateASM, "${utxoHex}", utxoHex)
	return parseNFTCodeASM(asm)
}

// BuildMintScript 对齐 NFT.buildMintScript。
func BuildMintScript(address string) (*bscript.Script, error) {
	addr, err := bscript.NewAddressFromString(address)
	if err != nil {
		return nil, err
	}
	asm := fmt.Sprintf("OP_DUP OP_HASH160 0x14 0x%s OP_EQUALVERIFY OP_CHECKSIG OP_RETURN 0x0d 0x5630204d696e74204e486f6c64", addr.PublicKeyHash)
	return parseNFTCodeASM(asm)
}

// BuildNFTHoldScript 对齐 NFT.buildHoldScript（勿与 MultiSig.BuildHoldScript 混淆）。
func BuildNFTHoldScript(address string) (*bscript.Script, error) {
	addr, err := bscript.NewAddressFromString(address)
	if err != nil {
		return nil, err
	}
	asm := fmt.Sprintf("OP_DUP OP_HASH160 0x14 0x%s OP_EQUALVERIFY OP_CHECKSIG OP_RETURN 0x0d 0x56302043757272204e486f6c64", addr.PublicKeyHash)
	return parseNFTCodeASM(asm)
}

// BuildNFTTapeScript 对齐 NFT.buildTapeScript（CollectionData | NFTData JSON）。
func BuildNFTTapeScript(data interface{}) (*bscript.Script, error) {
	j, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	asm := fmt.Sprintf("OP_FALSE OP_RETURN %s 4e54617065", hex.EncodeToString(j))
	return bscript.NewFromASM(asm)
}

// EncodeNFTDataToHex 对齐 NFT.encodeNFTDataToHex。
func EncodeNFTDataToHex(data interface{}) (string, error) {
	j, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(j), nil
}

// DecodeNFTDataFromHex 对齐 NFT.decodeNFTDataFromHex。
func DecodeNFTDataFromHex(h string) (map[string]interface{}, error) {
	raw, err := hex.DecodeString(h)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode nft json: %w", err)
	}
	return m, nil
}

// BuildNFTUnlockScript 对齐 NFT.buildUnlockScript。
func BuildNFTUnlockScript(priv *bec.PrivateKey, currentTX *bt.Tx, preTX *bt.Tx, prePreTx *bt.Tx, currentUnlockIndex uint32) (*bscript.Script, error) {
	cur, err := util.GetNFTCurrentTxdata(currentTX)
	if err != nil {
		return nil, err
	}
	prepre, err := util.GetNFTPrePreTxdata(prePreTx)
	if err != nil {
		return nil, err
	}
	pre, err := util.GetNFTPreTxdata(preTX)
	if err != nil {
		return nil, err
	}
	sh, err := currentTX.CalcInputSignatureHash(currentUnlockIndex, sighash.AllForkID)
	if err != nil {
		return nil, err
	}
	sig, err := priv.Sign(sh)
	if err != nil {
		return nil, err
	}
	sigBytes := sig.Serialise()
	sigBytes = append(sigBytes, byte(sighash.AllForkID))
	pub := priv.PubKey().SerialiseCompressed()
	txdata, err := hex.DecodeString(cur + prepre + pre)
	if err != nil {
		return nil, err
	}
	var sb bscript.Script
	if err := sb.AppendPushData(sigBytes); err != nil {
		return nil, err
	}
	if err := sb.AppendPushData(pub); err != nil {
		return nil, err
	}
	return bscript.NewFromBytes(append(sb.Bytes(), txdata...)), nil
}

type nftIn0Unlocker struct {
	priv          *bec.PrivateKey
	preTx, prePre *bt.Tx
	useV0Txdata   bool
}

func (u *nftIn0Unlocker) UnlockingScript(ctx context.Context, tx *bt.Tx, p bt.UnlockerParams) (*bscript.Script, error) {
	shf := p.SigHashFlags
	if shf == 0 {
		shf = sighash.AllForkID
	}
	var cur, prepre, pre string
	var err error
	if u.useV0Txdata {
		cur, err = util.GetNFTCurrentTxdataV0(tx)
		if err != nil {
			return nil, err
		}
		prepre, err = util.GetNFTPrePreTxdataV0(u.prePre)
		if err != nil {
			return nil, err
		}
		pre, err = util.GetNFTPreTxdataV0(u.preTx)
	} else {
		cur, err = util.GetNFTCurrentTxdata(tx)
		if err != nil {
			return nil, err
		}
		prepre, err = util.GetNFTPrePreTxdata(u.prePre)
		if err != nil {
			return nil, err
		}
		pre, err = util.GetNFTPreTxdata(u.preTx)
		if err != nil {
			return nil, err
		}
	}
	sh, err := tx.CalcInputSignatureHash(p.InputIdx, shf)
	if err != nil {
		return nil, err
	}
	sig, err := u.priv.Sign(sh)
	if err != nil {
		return nil, err
	}
	sigBytes := sig.Serialise()
	sigBytes = append(sigBytes, byte(shf))
	pub := u.priv.PubKey().SerialiseCompressed()
	txdata, err := hex.DecodeString(cur + prepre + pre)
	if err != nil {
		return nil, err
	}
	var sb bscript.Script
	if err := sb.AppendPushData(sigBytes); err != nil {
		return nil, err
	}
	if err := sb.AppendPushData(pub); err != nil {
		return nil, err
	}
	return bscript.NewFromBytes(append(sb.Bytes(), txdata...)), nil
}

type nftIn1Unlocker struct{ priv *bec.PrivateKey }

func (u *nftIn1Unlocker) UnlockingScript(ctx context.Context, tx *bt.Tx, p bt.UnlockerParams) (*bscript.Script, error) {
	shf := p.SigHashFlags
	if shf == 0 {
		shf = sighash.AllForkID
	}
	sh, err := tx.CalcInputSignatureHash(p.InputIdx, shf)
	if err != nil {
		return nil, err
	}
	sig, err := u.priv.Sign(sh)
	if err != nil {
		return nil, err
	}
	return bscript.NewP2PKHUnlockingScript(u.priv.PubKey().SerialiseCompressed(), sig.Serialise(), shf)
}

// p2pkhOrMintPrefixUnlocker 用于 createNFT / batchCreateNFT 首笔 mint 输入：锁定脚本为 P2PKH + OP_RETURN 后缀，
// IsP2PKH() 为 false，unlocker.Simple 会报 currently only p2pkh supported；此处按 PublicKeyHash 提取后与 JS setInputScript 一致。
type p2pkhOrMintPrefixUnlocker struct{ priv *bec.PrivateKey }

func (u *p2pkhOrMintPrefixUnlocker) UnlockingScript(ctx context.Context, tx *bt.Tx, p bt.UnlockerParams) (*bscript.Script, error) {
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

type p2pkhOrMintPrefixUnlockerGetter struct{ priv *bec.PrivateKey }

func (g *p2pkhOrMintPrefixUnlockerGetter) Unlocker(ctx context.Context, lockingScript *bscript.Script) (bt.Unlocker, error) {
	return &p2pkhOrMintPrefixUnlocker{priv: g.priv}, nil
}

type nftTransferUnlockerGetter struct {
	priv          *bec.PrivateKey
	preTx, prePre *bt.Tx
	useV0         bool
	step          int
}

func (g *nftTransferUnlockerGetter) Unlocker(ctx context.Context, ls *bscript.Script) (bt.Unlocker, error) {
	i := g.step
	g.step++
	switch i {
	case 0:
		return &nftIn0Unlocker{priv: g.priv, preTx: g.preTx, prePre: g.prePre, useV0Txdata: g.useV0}, nil
	case 1:
		return &nftIn1Unlocker{priv: g.priv}, nil
	default:
		return &unlocker.Simple{PrivateKey: g.priv}, nil
	}
}

// CreateCollection 对齐 NFT.createCollection，返回未签名交易 hex。
func CreateCollection(address string, priv *bec.PrivateKey, data *CollectionData, utxos []*bt.UTXO) (string, error) {
	if data.Supply < 0 || data.Supply > 100000 {
		return "", fmt.Errorf("invalid supply")
	}
	tape, err := BuildNFTTapeScript(data)
	if err != nil {
		return "", err
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	for i := 0; i < data.Supply; i++ {
		ms, err := BuildMintScript(address)
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: ms, Satoshis: 100})
	}
	// 与 NFT.createCollection 一致：tx.change(address) 使用首参 address（铸币槽接收方），非签名私钥对应地址。
	if err := tx.ChangeToAddress(address, newFeeQuoteNFT()); err != nil {
		return "", err
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: priv}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// CreateNFT 对齐 NFT.createNFT。
func CreateNFT(collectionID string, address string, priv *bec.PrivateKey, data *NFTData, utxos []*bt.UTXO, nftUtxo *bt.UTXO) (string, error) {
	if data.File == "" {
		voutBuf := make([]byte, 4)
		binary.LittleEndian.PutUint32(voutBuf, nftUtxo.Vout)
		data.File = collectionID + hex.EncodeToString(voutBuf)
	}
	tape, err := BuildNFTTapeScript(data)
	if err != nil {
		return "", err
	}
	hold, err := BuildNFTHoldScript(address)
	if err != nil {
		return "", err
	}
	code, err := BuildCodeScript(nftUtxo.TxIDStr(), nftUtxo.Vout)
	if err != nil {
		return "", err
	}
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), true)
	if err != nil {
		return "", err
	}
	inputs := append([]*bt.UTXO{nftUtxo}, utxos...)
	tx := newFTTx()
	if err := tx.FromUTXOs(inputs...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: 200})
	tx.AddOutput(&bt.Output{LockingScript: hold, Satoshis: 100})
	tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	if err := tx.ChangeToAddress(addr.AddressString, newFeeQuoteNFT()); err != nil {
		return "", err
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &p2pkhOrMintPrefixUnlockerGetter{priv: priv}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

func nftAddrMainnet(network string) bool {
	n := strings.TrimSpace(strings.ToLower(network))
	return n == "" || n == "mainnet"
}

// BatchCreateNFT 对齐 NFT.batchCreateNFT：按 mint UTXO 与（首笔的）手续费 UTXO、或上一笔找零串联批量铸币。
// 每笔交易输出布局与 CreateNFT 相同：code 200 / hold 100 / tape 0 / change；第 i>0 笔额外引用 txs[i-1] 的 output[3] 作为输入之一。
// network 为 testnet / mainnet / 与 api 一致，用于找零地址版本。
func BatchCreateNFT(collectionID string, address string, priv *bec.PrivateKey, datas []NFTData, utxos []*bt.UTXO, nftUtxos []*bt.UTXO, network string) ([]string, error) {
	if len(datas) != len(nftUtxos) {
		return nil, fmt.Errorf("datas length must match nftUtxos length")
	}
	if len(datas) == 0 {
		return nil, fmt.Errorf("empty batch")
	}
	hold, err := BuildNFTHoldScript(address)
	if err != nil {
		return nil, err
	}
	addr, err := bscript.NewAddressFromPublicKey(priv.PubKey(), nftAddrMainnet(network))
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	out := make([]string, 0, len(datas))
	var prevTx *bt.Tx
	for i := range datas {
		d := datas[i]
		voutBuf := make([]byte, 4)
		binary.LittleEndian.PutUint32(voutBuf, nftUtxos[i].Vout)
		if strings.TrimSpace(d.File) == "" {
			d.File = collectionID + hex.EncodeToString(voutBuf)
		}
		tape, err := BuildNFTTapeScript(&d)
		if err != nil {
			return nil, err
		}
		code, err := BuildCodeScript(nftUtxos[i].TxIDStr(), nftUtxos[i].Vout)
		if err != nil {
			return nil, err
		}
		tx := newFTTx()
		if err := tx.From(nftUtxos[i].TxIDStr(), nftUtxos[i].Vout, nftUtxos[i].LockingScript.String(), nftUtxos[i].Satoshis); err != nil {
			return nil, err
		}
		if i == 0 {
			if len(utxos) == 0 {
				return nil, fmt.Errorf("utxos required for first batch tx")
			}
			if err := tx.FromUTXOs(utxos...); err != nil {
				return nil, err
			}
		} else {
			if prevTx == nil || len(prevTx.Outputs) <= 3 {
				return nil, fmt.Errorf("previous batch tx missing change output")
			}
			o := prevTx.Outputs[3]
			if err := tx.From(prevTx.TxID(), 3, o.LockingScript.String(), o.Satoshis); err != nil {
				return nil, err
			}
		}
		tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: 200})
		tx.AddOutput(&bt.Output{LockingScript: hold, Satoshis: 100})
		tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
		if err := tx.ChangeToAddress(addr.AddressString, newFeeQuoteNFT()); err != nil {
			return nil, err
		}
		if err := tx.FillAllInputs(ctx, &p2pkhOrMintPrefixUnlockerGetter{priv: priv}); err != nil {
			return nil, err
		}
		prevTx, err = bt.NewTxFromString(hex.EncodeToString(tx.Bytes()))
		if err != nil {
			return nil, err
		}
		out = append(out, hex.EncodeToString(tx.Bytes()))
	}
	return out, nil
}

// TransferNFT 对齐 NFT.transferNFT。
func (n *NFT) TransferNFT(addressFrom, addressTo string, priv *bec.PrivateKey, utxos []*bt.UTXO, preTx, prePreTx *bt.Tx, batch bool) (string, error) {
	code, err := BuildCodeScript(n.CollectionID, uint32(n.CollectionIndex))
	if err != nil {
		return "", err
	}
	hold, err := BuildNFTHoldScript(addressTo)
	if err != nil {
		return "", err
	}
	tape, err := BuildNFTTapeScript(n.NftData)
	if err != nil {
		return "", err
	}
	tx := newFTTx()
	if err := tx.From(preTx.TxID(), 0, preTx.Outputs[0].LockingScript.String(), preTx.Outputs[0].Satoshis); err != nil {
		return "", err
	}
	if err := tx.From(preTx.TxID(), 1, preTx.Outputs[1].LockingScript.String(), preTx.Outputs[1].Satoshis); err != nil {
		return "", err
	}
	if len(utxos) > 0 {
		if err := tx.FromUTXOs(utxos...); err != nil {
			return "", err
		}
	}
	tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: 200})
	tx.AddOutput(&bt.Output{LockingScript: hold, Satoshis: 100})
	tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	if !batch {
		if err := tx.ChangeToAddress(addressFrom, newFeeQuoteNFT()); err != nil {
			return "", err
		}
	} else if len(utxos) > 0 {
		if err := tx.ChangeToAddress(addressFrom, newFeeQuoteNFT()); err != nil {
			return "", err
		}
	}
	ctx := context.Background()
	ug := &nftTransferUnlockerGetter{priv: priv, preTx: preTx, prePre: prePreTx, useV0: false}
	if err := tx.FillAllInputs(ctx, ug); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// TransferNFTWithTBC 对齐 NFT.transferNFTWithTBC（tbcAmount 为带 6 位小数的 TBC 数量）。
func (n *NFT) TransferNFTWithTBC(addressFrom, addressToNft, addressToTbc string, priv *bec.PrivateKey,
	utxos []*bt.UTXO, preTx, prePreTx *bt.Tx, tbcAmount float64) (string, error) {
	code, err := BuildCodeScript(n.CollectionID, uint32(n.CollectionIndex))
	if err != nil {
		return "", err
	}
	hold, err := BuildNFTHoldScript(addressToNft)
	if err != nil {
		return "", err
	}
	tape, err := BuildNFTTapeScript(n.NftData)
	if err != nil {
		return "", err
	}
	amt := util.ParseDecimalToBigInt(fmt.Sprintf("%g", tbcAmount), 6)
	if amt.Sign() <= 0 {
		return "", fmt.Errorf("invalid tbc amount")
	}
	if !amt.IsUint64() {
		return "", fmt.Errorf("tbc amount too large")
	}
	sat := amt.Uint64()

	tx := newFTTx()
	if err := tx.From(preTx.TxID(), 0, preTx.Outputs[0].LockingScript.String(), preTx.Outputs[0].Satoshis); err != nil {
		return "", err
	}
	if err := tx.From(preTx.TxID(), 1, preTx.Outputs[1].LockingScript.String(), preTx.Outputs[1].Satoshis); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: 200})
	tx.AddOutput(&bt.Output{LockingScript: hold, Satoshis: 100})
	tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	if err := tx.PayToAddress(addressToTbc, sat); err != nil {
		return "", err
	}
	if err := tx.ChangeToAddress(addressFrom, newFeeQuoteNFT()); err != nil {
		return "", err
	}
	ctx := context.Background()
	ug := &nftTransferUnlockerGetter{priv: priv, preTx: preTx, prePre: prePreTx, useV0: false}
	if err := tx.FillAllInputs(ctx, ug); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// TransferNFTV0 对齐 NFT.transferNFT_v0（解锁路径使用 v0 txdata）。
func (n *NFT) TransferNFTV0(addressFrom, addressTo string, priv *bec.PrivateKey, utxos []*bt.UTXO, preTx, prePreTx *bt.Tx) (string, error) {
	code, err := BuildCodeScriptV0(n.CollectionID, uint32(n.CollectionIndex))
	if err != nil {
		return "", err
	}
	hold, err := BuildNFTHoldScript(addressTo)
	if err != nil {
		return "", err
	}
	tape, err := BuildNFTTapeScript(n.NftData)
	if err != nil {
		return "", err
	}
	tx := newFTTx()
	if err := tx.From(preTx.TxID(), 0, preTx.Outputs[0].LockingScript.String(), preTx.Outputs[0].Satoshis); err != nil {
		return "", err
	}
	if err := tx.From(preTx.TxID(), 1, preTx.Outputs[1].LockingScript.String(), preTx.Outputs[1].Satoshis); err != nil {
		return "", err
	}
	if len(utxos) > 0 {
		if err := tx.FromUTXOs(utxos...); err != nil {
			return "", err
		}
	}
	tx.AddOutput(&bt.Output{LockingScript: code, Satoshis: 200})
	tx.AddOutput(&bt.Output{LockingScript: hold, Satoshis: 100})
	tx.AddOutput(&bt.Output{LockingScript: tape, Satoshis: 0})
	if err := tx.ChangeToAddress(addressFrom, newFeeQuoteNFT()); err != nil {
		return "", err
	}
	ctx := context.Background()
	ug := &nftTransferUnlockerGetter{priv: priv, preTx: preTx, prePre: prePreTx, useV0: true}
	if err := tx.FillAllInputs(ctx, ug); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}
