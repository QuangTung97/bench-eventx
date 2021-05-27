.PHONY: all install-tools generate lint

all:
	go build -o server cmd/server/main.go
	go build -o client cmd/client/main.go
	go build -o bench cmd/bench/main.go

install-tools:
	go install \
        github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway \
        github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2 \
        google.golang.org/protobuf/cmd/protoc-gen-go \
        google.golang.org/grpc/cmd/protoc-gen-go-grpc

GENERATED := benchpb

generate:
	cd proto && \
	protoc -I . \
		--go_out ../${GENERATED} --go_opt paths=source_relative \
		--go-grpc_out ../${GENERATED} --go-grpc_opt paths=source_relative \
		--grpc-gateway_out ../${GENERATED} --grpc-gateway_opt logtostderr=true --grpc-gateway_opt paths=source_relative \
		bench.proto

lint:
	go fmt ./...
	go vet ./...
	golint ./...
	errcheck ./...
