VERSION ?= 0.3.0
BINARY  := op-forward
LDFLAGS := -s -w -X github.com/ekovshilovsky/op-forward/cmd.Version=$(VERSION)

.PHONY: build build-all clean test

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

build-all: clean
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_$(VERSION)_darwin_arm64/$(BINARY) .
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_$(VERSION)_darwin_amd64/$(BINARY) .
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_$(VERSION)_linux_arm64/$(BINARY) .
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_$(VERSION)_linux_amd64/$(BINARY) .
	cd dist && for d in */; do tar -czf "$${d%/}.tar.gz" -C "$$d" $(BINARY); done

test:
	go test ./...

clean:
	rm -rf $(BINARY) dist/
