.PHONY: test vet build-linux clean

test:
	go test -race ./...

vet:
	go vet ./...

build-linux:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/tailrpproxy-linux-amd64 ./cmd/tailrpproxy
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/tailrpproxy-linux-arm64 ./cmd/tailrpproxy

clean:
	rm -rf dist
