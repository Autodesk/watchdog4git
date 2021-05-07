VERSION=2.0.0

PURPOSE="check for correct LFS usage and make suggestions about LFS files"
NAME=lfswatchdog

default: fmt tidy build test

build: 
	go build -o $(NAME) -v

fmt:
	find . -type f -iname '*.go' -not -path './vendor/*' -exec go fmt {} \;

test:
	go test -v ./...

clean:
	go clean
	git clean -fxd

tag:
	git tag -a $(VERSION) -m "LFS watchdog $(VERSION)"

container:
	docker build -t lfswatchdog:$(VERSION) .

tidy:
	go mod tidy
