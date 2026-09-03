.PHONY: test vet version build build-linux clean

test:
	go test -race ./...

vet:
	go vet ./...

version:
	@./hack/version.sh

build:
	./hack/build.sh

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 OUTPUT=dist/tailrpproxy-linux-amd64 ./hack/build.sh
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 OUTPUT=dist/tailrpproxy-linux-arm64 ./hack/build.sh

clean:
	rm -rf dist
