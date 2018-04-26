.PHONY: all test

all: bin/stream-client test

bin/stream-client:
	go build --race -o bin/stream-client

# Note: --count=1 is needed to prevent test results caching. "go help test"
# says --cound=1 is the idiomatic way to do that. Doesn't look too obvious
# though, but okay.
test:
	go test --count=1 --race .
