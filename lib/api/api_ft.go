package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"

	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

type FtInfo struct {
	CodeScript  string `json:"codeScript"`
	TapeScript  string `json:"tapeScript"`
	TotalSupply string `json:"totalSupply"`
	Decimal     uint   `json:"decimal"`
	Name        string `json:"name"`
	Symbol      string `json:"symbol"`
}

type ftBalanceResponse struct {
	Data struct {
		Balance json.RawMessage `json:"balance"`
	} `json:"data"`
}

type ftUtxoRaw struct {
	TxID     string          `json:"txid"`
	Index    int             `json:"index"`
	TBCValue uint64          `json:"tbc_value"`
	FTValue  json.RawMessage `json:"ft_value"`
}

type ftUtxoListResponse struct {
	Data struct {
		UTXOs []ftUtxoRaw `json:"utxos"`
	} `json:"data"`
}

type ftInfoResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Data struct {
		CodeScript string `json:"code_script"`
		TapeScript string `json:"tape_script"`
		Amount     json.RawMessage `json:"amount"`
		Decimal    uint   `json:"decimal"`
		Name       string `json:"name"`
		Symbol     string `json:"symbol"`
	} `json:"data"`
}

// buildAddressOrHash 将地址/哈希转为 combinescript 路径段（链上 recipient 为 mode||payload 时的布局：00||pkh、01||hash）。
// 与 tbc-contract/lib/contract/ft 中脚本嵌入一致；部分索引器仅按此键建 combinescript。
// 查询顺序上作为 **补充**：在 JS 主路径（buildAddressOrHashLegacy）无结果后再试。
func buildAddressOrHash(addressOrHash string) (string, error) {
	ok, _ := bscript.ValidateAddress(addressOrHash)
	if ok {
		addr, err := bscript.NewAddressFromString(addressOrHash)
		if err != nil {
			return "", err
		}
		return "00" + addr.PublicKeyHash, nil
	}
	if len(addressOrHash) == 40 && isHex(addressOrHash) {
		return "01" + addressOrHash, nil
	}
	return "", fmt.Errorf("Invalid address or hash")
}

// buildAddressOrHashLegacy 与 tbc-contract lib/api/api.ts 中 FT 查询一致（主路径）：地址为 publicKeyHash||"00"，裸哈希为 hash||"01"。
// GetFTBalance / FetchFtUTXOList 优先使用本函数，与 JS 行为对齐；无余额时再试 buildAddressOrHash。
func buildAddressOrHashLegacy(addressOrHash string) (string, error) {
	ok, _ := bscript.ValidateAddress(addressOrHash)
	if ok {
		addr, err := bscript.NewAddressFromString(addressOrHash)
		if err != nil {
			return "", err
		}
		return addr.PublicKeyHash + "00", nil
	}
	if len(addressOrHash) == 40 && isHex(addressOrHash) {
		return addressOrHash + "01", nil
	}
	return "", fmt.Errorf("Invalid address or hash")
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// enrichFtUtxoScriptsFromChain 用父交易真实输出的 locking script 与金额覆盖 Script / Satoshis。
// 索引器返回的列表不含 script；且 tbc_value 可能与链上 output 金额不一致。
// tx.FromUTXOs 将 Satoshis 写入 PreviousTxSigHash(BIP143) 所需字段，必须与 GetPreTxdata(preTX) 使用的
// tx.Outputs[vout].Satoshis 一致，否则签名 preimage 与 unlock 中的 pretxdata 不匹配（OP_EQUALVERIFY 等）。
func enrichFtUtxoScriptsFromChain(list []*bt.FtUTXO, network string) error {
	txCache := make(map[string]*bt.Tx)
	for _, u := range list {
		if u == nil {
			continue
		}
		tx, ok := txCache[u.TxID]
		if !ok {
			var err error
			tx, err = FetchTXRaw(u.TxID, network)
			if err != nil {
				return fmt.Errorf("enrich FtUTXO script: fetch tx %s: %w", u.TxID, err)
			}
			txCache[u.TxID] = tx
		}
		if int(u.Vout) >= len(tx.Outputs) {
			return fmt.Errorf("enrich FtUTXO script: vout %d out of range for tx %s (outputs=%d)", u.Vout, u.TxID, len(tx.Outputs))
		}
		out := tx.Outputs[u.Vout]
		ls := out.LockingScript
		if ls == nil {
			return fmt.Errorf("enrich FtUTXO script: tx %s vout %d has nil locking script", u.TxID, u.Vout)
		}
		u.Script = hex.EncodeToString(ls.Bytes())
		u.Satoshis = out.Satoshis
	}
	return nil
}

func parseBigIntOrUint64(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "0", nil
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return "0", nil
		}
		if _, ok := new(big.Int).SetString(s, 10); !ok {
			return "", fmt.Errorf("invalid decimal integer string: %q", s)
		}
		return s, nil
	}
	// JSON number：必须用 UseNumber，禁止先落 float64/uint64，否则 >2^53 的 ft_value 会丢精度，
	// tapeAmountSum 变小 → Transfer 误判「已凑满所选 UTXO」不写找零，广播后索引显示余额为 0。
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	n, ok := v.(json.Number)
	if !ok {
		return "", fmt.Errorf("expected JSON number, got %T", v)
	}
	s := strings.TrimSpace(n.String())
	if s == "" {
		return "0", nil
	}
	if _, ok := new(big.Int).SetString(s, 10); !ok {
		return "", fmt.Errorf("invalid JSON integer: %q", s)
	}
	return s, nil
}

func getFTBalanceByHash(contractTxID, hash, network string) (string, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sft/tokenbalance/combinescript/%s/contract/%s", baseURL, hash, contractTxID)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return "", fmt.Errorf("%w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var r ftBalanceResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("解析 FT 余额响应失败: %w", err)
	}
	return parseBigIntOrUint64(r.Data.Balance)
}

// getFTBalanceByAddress 请求 ft/tokenbalance/address/{address}/contract/{contractTxID}。
// testnet 等环境下该接口返回的 balance 可能与索引主键（00||pkh）不一致；见 GetFTBalance 回退逻辑。
func getFTBalanceByAddress(contractTxID, address, network string) (string, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sft/tokenbalance/address/%s/contract/%s", baseURL, address, contractTxID)
	resp, err := httpGetWithRetry(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var r ftBalanceResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("解析 FT 按地址余额响应失败: %w", err)
	}
	return parseBigIntOrUint64(r.Data.Balance)
}

func ftBalanceStringPositive(s string) bool {
	b, ok := new(big.Int).SetString(s, 10)
	return ok && b.Sign() > 0
}

// getFTBalanceCombinescriptDual 先 legacy（pkh||00）再 00||pkh，与链上 FT 脚本及索引主键对齐。
func getFTBalanceCombinescriptDual(contractTxID, addressOrHash, network string) (string, error) {
	hashJS, err := buildAddressOrHashLegacy(addressOrHash)
	if err != nil {
		return "", err
	}
	bal, err := getFTBalanceByHash(contractTxID, hashJS, network)
	if err != nil {
		return "", err
	}
	if ftBalanceStringPositive(bal) {
		return bal, nil
	}
	hashAlt, err2 := buildAddressOrHash(addressOrHash)
	if err2 != nil || hashAlt == hashJS {
		return bal, nil
	}
	bal2, err3 := getFTBalanceByHash(contractTxID, hashAlt, network)
	if err3 != nil {
		return bal, nil
	}
	return bal2, nil
}

// GetFTBalance 查询 FT 余额。对**有效地址**优先走按地址接口；若余额为 0 或请求失败，再使用 combinescript 双路径与索引真实数据对齐。
// 稳定币索引器以「首铸 mint 的 input0 父交易」(coin NFT txid) 为 stablecoinid；与 ft/.../contract/{mint} 不同，故在 FT 路径无正余额时再试 stablecoin/tokenbalance。
func GetFTBalance(contractTxID, addressOrHash, network string) (string, error) {
	ok, _ := bscript.ValidateAddress(addressOrHash)
	if ok {
		balAddr, err := getFTBalanceByAddress(contractTxID, addressOrHash, network)
		if err == nil && ftBalanceStringPositive(balAddr) {
			return balAddr, nil
		}
		if sid, err2 := StableCoinIndexerIDFromMintContractTx(contractTxID, network); err2 == nil && sid != "" {
			if b, err3 := getStableCoinTokenBalanceByAddress(sid, addressOrHash, network); err3 == nil && ftBalanceStringPositive(b) {
				return b, nil
			}
			if b, err4 := getStableCoinTokenBalanceCombinescriptDual(sid, addressOrHash, network); err4 == nil && ftBalanceStringPositive(b) {
				return b, nil
			}
		}
		return getFTBalanceCombinescriptDual(contractTxID, addressOrHash, network)
	}
	bal, err := getFTBalanceCombinescriptDual(contractTxID, addressOrHash, network)
	if err != nil {
		return "", err
	}
	if ftBalanceStringPositive(bal) {
		return bal, nil
	}
	if sid, err2 := StableCoinIndexerIDFromMintContractTx(contractTxID, network); err2 == nil && sid != "" {
		if b, err3 := getStableCoinTokenBalanceCombinescriptDual(sid, addressOrHash, network); err3 == nil && ftBalanceStringPositive(b) {
			return b, nil
		}
	}
	return bal, nil
}

func fetchFtUTXOListResponse(contractTxID, hash, network string) (ftUtxoListResponse, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sft/utxo/combinescript/%s/contract/%s", baseURL, hash, contractTxID)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return ftUtxoListResponse{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ftUtxoListResponse{}, err
	}

	var r ftUtxoListResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return ftUtxoListResponse{}, fmt.Errorf("解析 FT UTXO 响应失败: %w", err)
	}
	return r, nil
}

func FetchFtUTXOList(contractTxID, addressOrHash, codeScript, network string) ([]*bt.FtUTXO, error) {
	hashJS, err := buildAddressOrHashLegacy(addressOrHash)
	if err != nil {
		return nil, err
	}
	r, err := fetchFtUTXOListResponse(contractTxID, hashJS, network)
	if err != nil {
		return nil, err
	}
	if len(r.Data.UTXOs) == 0 {
		hashAlt, err2 := buildAddressOrHash(addressOrHash)
		if err2 == nil && hashAlt != hashJS {
			r, err = fetchFtUTXOListResponse(contractTxID, hashAlt, network)
			if err != nil {
				return nil, err
			}
		}
	}
	// 稳定币：索引 UTXO 在 stablecoin/utxo/.../stablecoinid/{coin NFT txid}；contractTxID 常为首铸 mint，与 JS fetchCoinUTXOList 一致。
	if len(r.Data.UTXOs) == 0 {
		sid, errSC := StableCoinIndexerIDFromMintContractTx(contractTxID, network)
		if errSC == nil && sid != "" {
			rs, errU := fetchStableCoinUtxoListResponse(sid, hashJS, network)
			if errU == nil && len(rs.Data.UTXOs) > 0 {
				r = rs
			} else {
				hashAlt, err2 := buildAddressOrHash(addressOrHash)
				if err2 == nil && hashAlt != hashJS {
					rs2, errU2 := fetchStableCoinUtxoListResponse(sid, hashAlt, network)
					if errU2 == nil && len(rs2.Data.UTXOs) > 0 {
						r = rs2
					}
				}
			}
		}
	}
	if len(r.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("The ft balance in the account is zero.")
	}

	result := make([]*bt.FtUTXO, 0, len(r.Data.UTXOs))
	for i := range r.Data.UTXOs {
		fv, err := parseBigIntOrUint64(r.Data.UTXOs[i].FTValue)
		if err != nil {
			return nil, err
		}
		result = append(result, &bt.FtUTXO{
			TxID:      r.Data.UTXOs[i].TxID,
			Vout:      uint32(r.Data.UTXOs[i].Index),
			Script:    codeScript,
			Satoshis:  r.Data.UTXOs[i].TBCValue,
			FtBalance: fv,
		})
	}
	if err := enrichFtUtxoScriptsFromChain(result, network); err != nil {
		return nil, err
	}
	return result, nil
}

func FetchFtUTXO(contractTxID, addressOrHash, codeScript, network string, amount *big.Int) (*bt.FtUTXO, error) {
	list, err := FetchFtUTXOList(contractTxID, addressOrHash, codeScript, network)
	if err != nil {
		return nil, err
	}
	var selected *bt.FtUTXO
	for _, u := range list {
		ub, _ := new(big.Int).SetString(u.FtBalance, 10)
		if ub != nil && ub.Cmp(amount) >= 0 {
			selected = u
			break
		}
	}
	if selected == nil {
		selected = list[0]
	}
	sb, _ := new(big.Int).SetString(selected.FtBalance, 10)
	if sb != nil && sb.Cmp(amount) < 0 {
		totalStr, err := GetFTBalance(contractTxID, addressOrHash, network)
		if err != nil {
			return nil, err
		}
		tb, _ := new(big.Int).SetString(totalStr, 10)
		if tb != nil && tb.Cmp(amount) >= 0 {
			return nil, fmt.Errorf("Insufficient FTbalance, please merge FT UTXOs")
		}
		return nil, fmt.Errorf("FTbalance not enough!")
	}
	return selected, nil
}

func FetchFtUTXOs(contractTxID, addressOrHash, codeScript, network string, amount *big.Int) ([]*bt.FtUTXO, error) {
	list, err := FetchFtUTXOList(contractTxID, addressOrHash, codeScript, network)
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool {
		a, _ := new(big.Int).SetString(list[i].FtBalance, 10)
		b, _ := new(big.Int).SetString(list[j].FtBalance, 10)
		if a == nil {
			return true
		}
		if b == nil {
			return false
		}
		return a.Cmp(b) > 0
	})

	if amount == nil || amount.Sign() == 0 {
		max := 5
		if len(list) < max {
			max = len(list)
		}
		return list[:max], nil
	}

	sum := new(big.Int)
	var result []*bt.FtUTXO
	for i := 0; i < len(list) && i < 5; i++ {
		ub, _ := new(big.Int).SetString(list[i].FtBalance, 10)
		if ub != nil {
			sum.Add(sum, ub)
		}
		result = append(result, list[i])
		if sum.Cmp(amount) >= 0 {
			return result, nil
		}
	}
	totalStr, err := GetFTBalance(contractTxID, addressOrHash, network)
	if err != nil {
		return nil, err
	}
	tb, _ := new(big.Int).SetString(totalStr, 10)
	if tb != nil && tb.Cmp(amount) >= 0 {
		return nil, fmt.Errorf("Insufficient FTbalance, please merge FT UTXOs")
	}
	return nil, fmt.Errorf("FTbalance not enough!")
}

func FetchFtUTXOsForPool(contractTxID, addressOrHash, codeScript, network string, amount *big.Int, number int) ([]*bt.FtUTXO, error) {
	if number <= 0 {
		return nil, fmt.Errorf("Number must be a positive integer greater than 0")
	}
	list, err := FetchFtUTXOList(contractTxID, addressOrHash, codeScript, network)
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool {
		a, _ := new(big.Int).SetString(list[i].FtBalance, 10)
		b, _ := new(big.Int).SetString(list[j].FtBalance, 10)
		if a == nil {
			return true
		}
		if b == nil {
			return false
		}
		return a.Cmp(b) > 0
	})

	sum := new(big.Int)
	var result []*bt.FtUTXO
	for i := 0; i < len(list) && i < number; i++ {
		ub, _ := new(big.Int).SetString(list[i].FtBalance, 10)
		if ub != nil {
			sum.Add(sum, ub)
		}
		result = append(result, list[i])
		if i >= 1 && sum.Cmp(amount) >= 0 {
			break
		}
	}
	if sum.Cmp(amount) < 0 {
		totalStr, err := GetFTBalance(contractTxID, addressOrHash, network)
		if err != nil {
			return nil, err
		}
		tb, _ := new(big.Int).SetString(totalStr, 10)
		if tb != nil && tb.Cmp(amount) >= 0 {
			return nil, fmt.Errorf("Insufficient FTbalance, please merge FT UTXOs")
		}
		return nil, fmt.Errorf("FTbalance not enough!")
	}
	return result, nil
}

func FetchFtUTXOsMultiSig(contractTxID, addressOrHash, codeScript, network string) ([]*bt.FtUTXO, error) {
	list, err := FetchFtUTXOList(contractTxID, addressOrHash, codeScript, network)
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool {
		a, _ := new(big.Int).SetString(list[i].FtBalance, 10)
		b, _ := new(big.Int).SetString(list[j].FtBalance, 10)
		if a == nil {
			return true
		}
		if b == nil {
			return false
		}
		return a.Cmp(b) < 0
	})
	return list, nil
}

func findMinFiveSum(balances []*big.Int, target *big.Int) []int {
	n := len(balances)
	if n < 5 {
		return nil
	}
	minSum := new(big.Int).SetUint64(^uint64(0))
	var result []int
	for i := 0; i <= n-5; i++ {
		for j := i + 1; j <= n-4; j++ {
			left := j + 1
			right := n - 1
			for left < right-1 {
				sum := new(big.Int)
				sum.Add(sum, balances[i])
				sum.Add(sum, balances[j])
				sum.Add(sum, balances[left])
				sum.Add(sum, balances[right-1])
				sum.Add(sum, balances[right])
				if sum.Cmp(target) >= 0 && sum.Cmp(minSum) < 0 {
					minSum.Set(sum)
					result = []int{i, j, left, right - 1, right}
				}
				if sum.Cmp(target) < 0 {
					left++
				} else {
					right--
				}
			}
		}
	}
	return result
}

func GetFtUTXOsMultiSig(contractTxID, addressOrHash, codeScript, network string, amount *big.Int) ([]*bt.FtUTXO, error) {
	list, err := FetchFtUTXOsMultiSig(contractTxID, addressOrHash, codeScript, network)
	if err != nil {
		return nil, err
	}
	balances := make([]*big.Int, len(list))
	total := new(big.Int)
	for i := range list {
		b, _ := new(big.Int).SetString(list[i].FtBalance, 10)
		balances[i] = b
		if b != nil {
			total.Add(total, b)
		}
	}
	if total.Cmp(amount) < 0 {
		return nil, fmt.Errorf("Insufficient FT balance")
	}
	if len(list) <= 5 {
		return list, nil
	}
	indices := findMinFiveSum(balances, amount)
	if indices == nil {
		return nil, fmt.Errorf("Please merge MultiSig UTXO")
	}
	return []*bt.FtUTXO{
		list[indices[0]],
		list[indices[1]],
		list[indices[2]],
		list[indices[3]],
		list[indices[4]],
	}, nil
}

func FetchFtInfo(contractTxID, network string) (*FtInfo, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sft/info/contract/%s", baseURL, contractTxID)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var r ftInfoResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("解析 FT Info 响应失败: %w", err)
	}
	if r.Code != "" && r.Code != "200" {
		msg := r.Message
		if r.Error != "" {
			msg = r.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("code=%s", r.Code)
		}
		return nil, fmt.Errorf("FT Info 接口返回失败: %s", msg)
	}
	totalSupply, err := parseBigIntOrUint64(r.Data.Amount)
	if err != nil {
		return nil, fmt.Errorf("解析 FT Info amount 失败: %w", err)
	}
	if strings.TrimSpace(r.Data.CodeScript) == "" || strings.TrimSpace(r.Data.TapeScript) == "" {
		return nil, fmt.Errorf("FT Info 响应缺少 code/tape 脚本")
	}
	return &FtInfo{
		CodeScript:  r.Data.CodeScript,
		TapeScript:  r.Data.TapeScript,
		TotalSupply: totalSupply,
		Decimal:     r.Data.Decimal,
		Name:        r.Data.Name,
		Symbol:      r.Data.Symbol,
	}, nil
}

func FetchFtPrePreTxData(preTX *bt.Tx, preTxVout int, network string) (string, error) {
	if preTxVout+1 >= len(preTX.Outputs) {
		return "", fmt.Errorf("preTxVout+1 out of range")
	}
	tapeScript := preTX.Outputs[preTxVout+1].LockingScript.Bytes()
	if len(tapeScript) < 51 {
		return "", fmt.Errorf("tape script too short")
	}
	tapeSlice := tapeScript[3:51]
	tapeHex := hex.EncodeToString(tapeSlice)

	var prepretxdata string
	for i := len(tapeHex) - 16; i >= 0; i -= 16 {
		chunk := tapeHex[i : i+16]
		if chunk != "0000000000000000" {
			inputIndex := i / 16
			if inputIndex >= len(preTX.Inputs) {
				return "", fmt.Errorf("input index out of range")
			}
			// PreviousTxID 已由 Input.ReadFrom 转为与 TxID()/浏览器一致的序，勿再 ReverseBytes，否则 txraw 接口会 404+空 raw 并触发 ErrTxTooShort。
			prevTxID := hex.EncodeToString(preTX.Inputs[inputIndex].PreviousTxID())
			prepreTX, err := FetchTXRaw(prevTxID, network)
			if err != nil {
				return "", err
			}
			data, err := bt.GetPrePreTxdata(prepreTX, int(preTX.Inputs[inputIndex].PreviousTxOutIndex))
			if err != nil {
				return "", err
			}
			prepretxdata += data
		}
	}
	return "57" + prepretxdata, nil
}
