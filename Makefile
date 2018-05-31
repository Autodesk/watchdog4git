VERSION=1.1.0

PURPOSE="Checks commits pushed to GitHub for common Git problems"
NAME=watchdog4git

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOFMT=$(GOCMD) fmt
BINARY=$(NAME)
BINARY_WIN=$(NAME).exe
RELEASEDIR=release

# rules
#
default: fmt test build

build:
	$(GOBUILD) -o $(BINARY) -v

fmt:
	find . -type f -iname '*.go' -not -path './vendor/*' -exec $(GOFMT) {} \;

test:
	$(GOTEST) -v ./...

clean:
	$(GOCLEAN)
	rm -rf $(RELEASEDIR)
	git clean -fxd

deps:
	dep ensure

cross: cross_lnx cross_win cross_osx

cross_lnx:
	mkdir -p $(RELEASEDIR)/lnx
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GOBUILD) -o $(RELEASEDIR)/lnx/$(BINARY) -v

cross_osx:
	mkdir -p $(RELEASEDIR)/osx
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GOBUILD) -o $(RELEASEDIR)/osx/$(BINARY) -v

cross_win:
	mkdir -p $(RELEASEDIR)/win
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GOBUILD) -o $(RELEASEDIR)/win/$(BINARY_WIN) -v

tag:
	git tag -a $(VERSION) -m "Watchdog4Git $(VERSION)"

packaging: cross
	cp README.md $(RELEASEDIR)/lnx
	tar -zcf $(RELEASEDIR)/$(BINARY)-lnx.tar.gz -C $(RELEASEDIR)/lnx .
	cp README.md $(RELEASEDIR)/osx
	tar -zcf $(RELEASEDIR)/$(BINARY)-osx.tar.gz -C $(RELEASEDIR)/osx .
	cp README.md $(RELEASEDIR)/win
	zip -r $(RELEASEDIR)/$(BINARY)-win.zip -j $(RELEASEDIR)/win

static: build
	CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o $(BINARY) .
