# NAME is the binary name; the only other place the codename appears is the
# toolName constant in cmd/$(NAME)/main.go.
NAME := softmap

.PHONY: build test vet

build:
	go build -o bin/$(NAME) ./cmd/$(NAME)

test:
	go test ./...

vet:
	go vet ./...
