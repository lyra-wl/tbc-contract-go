package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

type NFTInfo struct {
	CollectionID         string `json:"collectionId"`
	CollectionIndex      int    `json:"collectionIndex"`
	CollectionName       string `json:"collectionName"`
	NftName              string `json:"nftName"`
	NftSymbol            string `json:"nftSymbol"`
	NftAttributes        string `json:"nft_attributes"`
	NftDescription       string `json:"nftDescription"`
	NftTransferTimeCount int    `json:"nftTransferTimeCount"`
	NftIcon              string `json:"nftIcon"`
}

type NFTUTXO struct {
	TxID     string
	Vout     uint32
	Script   string
	Satoshis uint64
}

type nftUtxoRaw struct {
	TxID  string `json:"txid"`
	Index int    `json:"index"`
	Value uint64 `json:"value"`
}

type nftUtxoListResponse struct {
	Data struct {
		UTXOs []nftUtxoRaw `json:"utxos"`
	} `json:"data"`
}

type nftInfoResponse struct {
	Data struct {
		CollectionID    string `json:"collection_id"`
		CollectionIndex int    `json:"collection_index"`
		CollectionName  string `json:"collection_name"`
		NftName         string `json:"nft_name"`
		NftSymbol       string `json:"nft_symbol"`
		NftAttributes   string `json:"nft_attributes"`
		NftDescription  string `json:"nft_description"`
		NftTransferCount int   `json:"nft_transfer_count"`
		NftIcon         string `json:"nft_icon"`
	} `json:"data"`
}

type nftListItem struct {
	NftHolder     string `json:"nft_holder"`
	NftContractID string `json:"nft_contract_id"`
}

type nftListResponse struct {
	Data struct {
		NftList []nftListItem `json:"nft_list"`
	} `json:"data"`
}

func FetchNFTUTXO(script, txHash, network string) (*NFTUTXO, error) {
	hash, err := scriptHashFromHex(script)
	if err != nil {
		return nil, err
	}
	baseURL := getBaseURL(network)
	// 与 tbc-contract lib/api/api.ts fetchNFTTXO 一致：带 tx_hash 时用 utxo/scriptpubkeyhash（全量再按 tx 过滤），
	// 否则用 nft/utxo/scriptpubkeyhash。
	var url string
	if strings.TrimSpace(txHash) != "" {
		url = fmt.Sprintf("%sutxo/scriptpubkeyhash/%s", baseURL, hash)
	} else {
		url = fmt.Sprintf("%snft/utxo/scriptpubkeyhash/%s", baseURL, hash)
	}

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Failed to fetch UTXO: %s", string(body))
	}

	var r nftUtxoListResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if len(r.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("No matching UTXO found.")
	}

	if txHash != "" {
		want := strings.TrimSpace(txHash)
		var filtered []nftUtxoRaw
		for _, u := range r.Data.UTXOs {
			if strings.EqualFold(strings.TrimSpace(u.TxID), want) {
				filtered = append(filtered, u)
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("No matching UTXO found.")
		}
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Index < filtered[j].Index
		})
		u := filtered[0]
		return &NFTUTXO{TxID: u.TxID, Vout: uint32(u.Index), Script: script, Satoshis: u.Value}, nil
	}
	u := r.Data.UTXOs[0]
	return &NFTUTXO{TxID: u.TxID, Vout: uint32(u.Index), Script: script, Satoshis: u.Value}, nil
}

func FetchNFTUTXOs(script, txHash, network string) ([]*NFTUTXO, error) {
	hash, err := scriptHashFromHex(script)
	if err != nil {
		return nil, err
	}
	baseURL := getBaseURL(network)
	// 与 api.ts fetchNFTTXOs 一致：始终 utxo/scriptpubkeyhash，再按 tx_hash 过滤
	url := fmt.Sprintf("%sutxo/scriptpubkeyhash/%s", baseURL, hash)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Failed to fetch UTXO: %s", string(body))
	}

	var r nftUtxoListResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}

	want := strings.TrimSpace(txHash)
	var filtered []nftUtxoRaw
	for _, u := range r.Data.UTXOs {
		if strings.EqualFold(strings.TrimSpace(u.TxID), want) {
			filtered = append(filtered, u)
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("The collection supply has been exhausted.")
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Index < filtered[j].Index
	})

	result := make([]*NFTUTXO, 0, len(filtered))
	for _, u := range filtered {
		result = append(result, &NFTUTXO{TxID: u.TxID, Vout: uint32(u.Index), Script: script, Satoshis: u.Value})
	}
	return result, nil
}

func FetchNFTInfo(contractID, network string) (*NFTInfo, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%snft/nftinfo/nftid/%s", baseURL, contractID)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Failed to fetch NFTInfo: %s", string(body))
	}

	var r nftInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &NFTInfo{
		CollectionID:         r.Data.CollectionID,
		CollectionIndex:      r.Data.CollectionIndex,
		CollectionName:       r.Data.CollectionName,
		NftName:              r.Data.NftName,
		NftSymbol:            r.Data.NftSymbol,
		NftAttributes:        r.Data.NftAttributes,
		NftDescription:       r.Data.NftDescription,
		NftTransferTimeCount: r.Data.NftTransferCount,
		NftIcon:              r.Data.NftIcon,
	}, nil
}

func FetchNFTs(collectionID, address string, start, end int, network string) ([]string, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%snft/nftbycollection/collectionid/%s/start/%d/end/%d", baseURL, collectionID, start, end)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Failed to fetch NFTs: %s", string(body))
	}

	var r nftListResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	var ids []string
	for _, n := range r.Data.NftList {
		if n.NftHolder == address {
			ids = append(ids, n.NftContractID)
		}
	}
	return ids, nil
}
