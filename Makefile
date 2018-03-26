
.PHONY: all
all: test

test:
	go test --race .
