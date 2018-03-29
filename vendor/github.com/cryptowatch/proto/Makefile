SHELL := /bin/bash

proto:
	protoc --go_out=Mmarkets/private.proto=github.com/cryptowatch/proto/markets:. markets/*.proto
	protoc --go_out=Mmarkets/private.proto=github.com/cryptowatch/proto/markets:. client/*.proto
	protoc --go_out=Mmarkets/market.proto=github.com/cryptowatch/proto/markets,Mmarkets/private.proto=github.com/cryptowatch/proto/markets:. stream/*.proto
