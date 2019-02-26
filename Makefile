.PHONY: all test

all: clean test stream-client trading-tui

stream-client:
	go build --race -o bin/stream-client ./cmd/stream-client

trading-tui:
	go build --race -o bin/trading-tui ./examples/trading-tui

# Note: --count=1 is needed to prevent test results caching. "go help test"
# says --cound=1 is the idiomatic way to do that. Doesn't look too obvious
# though, but okay.
test:
	go test --count=1 --race ./...

clean:
	rm -rf bin
