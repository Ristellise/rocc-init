BINARY := bin/rocc

.PHONY: build test vet fmt clean install

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

clean:
	rm -rf bin/

install: build
	install -m 0755 $(BINARY) /usr/local/bin/rocc
