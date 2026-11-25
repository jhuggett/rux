.PHONY: build run vet

# rux for this Mac, plus the Linux runner binary the server hands to guests.
build:
	go build -o dist/rux .
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/rux-linux-arm64 .

vet:
	go vet ./...

run: build
	./dist/rux serve
