CGO_ENABLED := 1
GO_TAGS     := fts5
BINARY      := moxie
LDFLAGS     := -s -w

.PHONY: build test clean install

build:
	CGO_ENABLED=$(CGO_ENABLED) go build -tags $(GO_TAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/moxie

test:
	CGO_ENABLED=$(CGO_ENABLED) go test -tags $(GO_TAGS) ./... -count=1

install:
	CGO_ENABLED=$(CGO_ENABLED) go install -tags $(GO_TAGS) -ldflags "$(LDFLAGS)" ./cmd/moxie

clean:
	rm -f $(BINARY)
