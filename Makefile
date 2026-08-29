.PHONY: build install clean test

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o build/alfaos ./cmd/alfaos/

install: build
	sudo install -m 755 build/alfaos /usr/local/bin/alfaos
	sudo mkdir -p /usr/share/alfaos/assets /etc/alfaos
	sudo cp -r assets/* /usr/share/alfaos/assets/
	sudo cp configs/default.yaml /etc/alfaos/config.yaml
	sudo ln -sf /usr/local/bin/alfaos /alfaos

clean:
	rm -rf build/

test:
	go vet ./...
	go build ./...
