//go:build integration
// +build integration

// 基于 docs/poolNFT2.0.md 的 PoolNFT2 集成测试。
//
// 运行：
//   cd ~/path/to/tbc-contract-go
//   export TBC_POOLNFT_CONTRACT_TXID=链上已存在的PoolNFT合约txid
//   export TBC_NETWORK=testnet
//   go test -tags=integration -v ./lib/contract -run TestPoolNFT2 -count=1
package contract

import (
	"math/big"
	"os"
	"strings"
	"testing"
)

func requirePoolNFTContractTxid(t *testing.T) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv("TBC_POOLNFT_CONTRACT_TXID"))
	if v == "" {
		t.Skip("设置 TBC_POOLNFT_CONTRACT_TXID 以运行 PoolNFT2 集成测试")
	}
	return v
}

// TestPoolNFT2_Integration_InitFromContractID 从链上合约 txid 初始化池状态
func TestPoolNFT2_Integration_InitFromContractID(t *testing.T) {
	contractTxid := requirePoolNFTContractTxid(t)
	network := envOrDefault("TBC_NETWORK", "testnet")

	pool := NewPoolNFT2(&PoolNFT2Config{
		ContractTxID: contractTxid,
		Network:      network,
	})

	if err := pool.InitFromContractID(); err != nil {
		t.Fatalf("InitFromContractID: %v", err)
	}

	t.Logf("PoolNFT2 初始化成功:")
	t.Logf("  ContractTxID:    %s", pool.ContractTxID)
	t.Logf("  FtAContractTxID: %s", pool.FtAContractTxID)
	t.Logf("  FtLpAmount:      %s", pool.FtLpAmount.String())
	t.Logf("  FtAAmount:       %s", pool.FtAAmount.String())
	t.Logf("  TbcAmount:       %s", pool.TbcAmount.String())
	t.Logf("  TbcAmountFull:   %s", pool.TbcAmountFull.String())
	t.Logf("  ServiceFeeRate:  %d", pool.ServiceFeeRate)
	t.Logf("  LpPlan:          %d", pool.LpPlan)
	t.Logf("  WithLock:        %v", pool.WithLock)
	t.Logf("  WithLockTime:    %v", pool.WithLockTime)
	t.Logf("  PoolVersion:     %d", pool.PoolVersion)

	if pool.FtAContractTxID == "" {
		t.Error("FtAContractTxID 应不为空")
	}
	if pool.FtLpAmount.Sign() <= 0 {
		t.Error("FtLpAmount 应大于0")
	}
	if pool.FtAAmount.Sign() <= 0 {
		t.Error("FtAAmount 应大于0")
	}
	if pool.TbcAmount.Sign() <= 0 {
		t.Error("TbcAmount 应大于0")
	}
}

// TestPoolNFT2_Integration_UpdatePoolNFT_IncreaseLP 测试 UpdatePoolNFT option=2 (TBC变化)
func TestPoolNFT2_Integration_UpdatePoolNFT_IncreaseLP(t *testing.T) {
	contractTxid := requirePoolNFTContractTxid(t)
	network := envOrDefault("TBC_NETWORK", "testnet")

	pool := NewPoolNFT2(&PoolNFT2Config{
		ContractTxID: contractTxid,
		Network:      network,
	})
	if err := pool.InitFromContractID(); err != nil {
		t.Fatalf("InitFromContractID: %v", err)
	}

	oldFtLp := new(big.Int).Set(pool.FtLpAmount)
	oldFtA := new(big.Int).Set(pool.FtAAmount)
	oldTbc := new(big.Int).Set(pool.TbcAmount)

	diff, err := pool.UpdatePoolNFT("1", 6, 2)
	if err != nil {
		t.Fatalf("UpdatePoolNFT: %v", err)
	}

	t.Logf("UpdatePoolNFT(1 TBC, option=2) 结果:")
	t.Logf("  FtLpDifference:    %s", diff.FtLpDifference.String())
	t.Logf("  FtADifference:     %s", diff.FtADifference.String())
	t.Logf("  TbcAmountDiff:     %s", diff.TbcAmountDifference.String())
	t.Logf("  TbcAmountFullDiff: %s", diff.TbcAmountFullDiff.String())

	if diff.TbcAmountDifference.Sign() <= 0 {
		t.Error("TBC 差值应大于0")
	}
	if pool.FtLpAmount.Cmp(oldFtLp) <= 0 {
		t.Error("增加 TBC 后 FtLpAmount 应增加")
	}
	if pool.FtAAmount.Cmp(oldFtA) <= 0 {
		t.Error("增加 TBC 后 FtAAmount 应增加")
	}
	if pool.TbcAmount.Cmp(oldTbc) <= 0 {
		t.Error("增加 TBC 后 TbcAmount 应增加")
	}
}

// TestPoolNFT2_Integration_GetPoolNftTape 测试 tape 生成
func TestPoolNFT2_Integration_GetPoolNftTape(t *testing.T) {
	contractTxid := requirePoolNFTContractTxid(t)
	network := envOrDefault("TBC_NETWORK", "testnet")

	pool := NewPoolNFT2(&PoolNFT2Config{
		ContractTxID: contractTxid,
		Network:      network,
	})
	if err := pool.InitFromContractID(); err != nil {
		t.Fatalf("InitFromContractID: %v", err)
	}

	tape, err := pool.GetPoolNftTape(pool.LpPlan, pool.WithLock, pool.WithLockTime)
	if err != nil {
		t.Fatalf("GetPoolNftTape: %v", err)
	}
	if tape == nil || tape.Len() == 0 {
		t.Fatal("GetPoolNftTape 返回空脚本")
	}
	t.Logf("PoolNFT tape 脚本长度: %d 字节", tape.Len())
}
