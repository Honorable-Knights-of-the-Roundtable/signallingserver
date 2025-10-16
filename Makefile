.PHONY: build dev clean

build:
	go build -o bin/signallingserver ./main.go

dev:
	air --build.cmd "go build -o bin/signallingserver main.go" \
		--build.full_bin "bin/signallingserver --configFilePath config.yaml"
clean:
	rm bin/*
