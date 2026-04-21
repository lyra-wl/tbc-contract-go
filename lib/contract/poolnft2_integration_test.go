//go:build integration
// +build integration

// 基于 docs/poolNFT2.0.md 的 PoolNFT2 集成测试。
//
// 运行（仅读链上池信息 / 本地公式）：
//
//	cd ~/path/to/tbc-contract-go
//	export TBC_POOLNFT_CONTRACT_TXID=链上已存在的PoolNFT合约txid
//	export TBC_NETWORK=testnet
//	go test -tags=integration -v ./lib/contract -run TestPoolNFT2 -count=1
//
// 全链路链上广播（建池 → 注资 → consumeLP → TBC↔Token 互换）需 Node + 同级目录 tbc-contract（已 npm install），并设置：
//
//	export RUN_POOLNFT2_FULL_CHAIN=1
//	export TBC_POOLNFT2_PRIVATE_KEY_WIF=操作地址 WIF（建池/init/swap 均用该地址）
//	export TBC_NETWORK=testnet
//	# 可选：export TBC_CONTRACT_ROOT=/绝对路径/tbc-contract
//	# 可选：export TBC_POOLNFT2_FT_CONTRACT_TXID=...（testnet 未设时脚本内默认 ade89567…dd93）
//	# 双钥与 create_poolnft.ts 一致时：主地址有 TBC、操作地址有 FT，则设
//	# export TBC_POOLNFT2_MAIN_PRIVATE_KEY_WIF=主地址WIF
//	go test -tags=integration -v ./lib/contract -run TestPoolNFT2_Integration_FullChainBroadcast -count=1
//
// TestPoolNFT2_Integration_FullChainBroadcast 在未设置下列变量时，会写入与 create_poolnft.ts 成功路径一致的默认值（仍可用 export 覆盖）：
//	TBC_POOLNFT2_FEE_TBC=0.01、INIT_TBC=25、INIT_FT=800、LP_PLAN=1、TAG=tenbillion_pool、SERVICE_RATE=25、INIT_UTXO_BUFFER_TBC=2、MINT_TESTNET_FT=1
//	MINT_TESTNET_FT=1 时脚本在 testnet 先自铸 FT 再建池（与 create_poolnft 新铸 FT 一致）；若已设 TBC_POOLNFT2_FT_CONTRACT_TXID 则不会自铸。
//
// 若 Cursor/沙箱下 go test 报 GOMODCACHE 文件缺失，可设置例如：
//	export GOMODCACHE=$PWD/../.gomodcache && mkdir -p "$GOMODCACHE" && go mod download
package contract

import (
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// TestPoolNFT2_Integration_FullChainBroadcast 调用 scripts/poolnft2_chain_flow.js，
// 对齐 poolNFT2.0.md：createPoolNFT（两笔广播）→ initPoolNFT → consumeLP → swaptoToken_baseTBC → swaptoTBC_baseToken。
// 私钥仅通过环境变量 TBC_POOLNFT2_PRIVATE_KEY_WIF 传入，勿写入源码。
func TestPoolNFT2_Integration_FullChainBroadcast(t *testing.T) {
	if strings.TrimSpace(os.Getenv("RUN_POOLNFT2_FULL_CHAIN")) != "1" {
		t.Skip(`设置 RUN_POOLNFT2_FULL_CHAIN=1 且 TBC_POOLNFT2_PRIVATE_KEY_WIF 后运行本用例`)
	}
	if strings.TrimSpace(os.Getenv("TBC_POOLNFT2_PRIVATE_KEY_WIF")) == "" {
		t.Fatal("需要环境变量 TBC_POOLNFT2_PRIVATE_KEY_WIF（WIF）")
	}
	// 与仓库根目录 create_poolnft.ts 小额 init 成功路径对齐（仅当环境变量未设置时生效）
	presets := map[string]string{
		"TBC_POOLNFT2_FEE_TBC":               "0.01",
		"TBC_POOLNFT2_INIT_TBC":            "25",
		"TBC_POOLNFT2_INIT_FT":             "800",
		"TBC_POOLNFT2_LP_PLAN":             "1",
		"TBC_POOLNFT2_TAG":                 "tenbillion_pool",
		"TBC_POOLNFT2_SERVICE_RATE":        "25",
		"TBC_POOLNFT2_INIT_UTXO_BUFFER_TBC": "2",
		"TBC_POOLNFT2_MINT_TESTNET_FT":     "1",
	}
	for k, v := range presets {
		if strings.TrimSpace(os.Getenv(k)) == "" {
			t.Setenv(k, v)
		}
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	script := filepath.Join(filepath.Dir(file), "..", "..", "scripts", "poolnft2_chain_flow.js")
	cmd := exec.Command("node", script)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	t.Logf("%s", string(out))
	if err != nil {
		t.Fatalf("poolnft2_chain_flow.js: %v", err)
	}
}
