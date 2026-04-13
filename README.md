# tbc-contract-go

TBC 合约的 Go 语言实现，对应 [tbc-contract](https://github.com/sCrypt-Inc/tbc-contract) 的 lib/contract 部分。

## 结构

```
tbc-contract-go/
├── go.mod
├── lib/
│   ├── contract/
│   │   └── ft.go      # FT 同质化代币合约
│   └── util/
│       └── util.go    # 工具函数 (BuildUTXO, BuildFtPrePreTxData 等)
└── README.md
```

## 依赖

- [tbc-lib-go](../tbc-lib-js-go/tbc-lib-go) - 通过 `replace` 使用本地路径

## FT 合约

### 创建实例

```go
// 从合约 txid 创建
ft, err := contract.NewFT("existing_contract_txid")

// 从参数创建新代币
ft, err := contract.NewFT(&contract.FtParams{
    Name:    "My Token",
    Symbol:  "MTK",
    Amount:  1000000,
    Decimal: 8,
})
```

### 初始化

```go
info, _ := api.FetchFtInfo(contractTxID, network)
ft.Initialize(&contract.FtInfo{
    Name:        info.Name,
    Symbol:      info.Symbol,
    Decimal:     int(info.Decimal),
    TotalSupply: totalSupply,
    CodeScript:  info.CodeScript,
    TapeScript:  info.TapeScript,
})
```

### 铸造

```go
txraws, err := ft.MintFT(privKey, addressTo, utxo)
// 返回 [txSourceRaw, txMintRaw]
```

### 转移

```go
txraw, err := ft.Transfer(privKey, addressTo, ftAmount, ftutxos, utxo, preTX, prepreTxData, tbcAmountSat)
```

## 扩展 tbc-lib-go

本仓库依赖的 tbc-lib-go 中已补充以下 FT 解锁相关函数：

- `GetPreTxdata` - 父交易数据
- `GetCurrentTxdata` - 当前交易数据
- `GetCurrentInputsdata` - 当前交易 inputs 数据
- `GetContractTxdata` - 合约交易数据
- `GetSizeHex` - 导出供合约使用的 size hex

## 注意事项

- 当前实现包含 MintFT、Transfer 及基础工具函数
- batchTransfer、mergeFT、transferWithAdditionalInfo 等高级方法可按需扩展
- getFTmintCode 中的完整脚本模板需与链上验证逻辑一致
