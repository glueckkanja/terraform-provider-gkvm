.PHONY: build test lint cover clean install

build:
	go build ./...

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

cover:
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

clean:
	rm -f coverage.out coverage.html terraform-provider-gkvm terraform-provider-gkvm.exe

install:
	go install .
