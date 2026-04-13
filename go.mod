module github.com/sCrypt-Inc/tbc-contract-go

go 1.17

require (
	github.com/libsv/go-bk v0.1.6
	github.com/sCrypt-Inc/go-bt/v2 v2.0.0
	golang.org/x/crypto v0.0.0-20210711020723-a769d52b0f97
)

require (
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.2.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
)

replace github.com/sCrypt-Inc/go-bt/v2 => ../tbc-lib-go
