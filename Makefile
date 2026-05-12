.PHONY: build dev clean

build:
	go build -o bin/signallingserver.exe ./main.go && cp config.yaml bin

run:
	cd bin && ./signallingserver.exe


dev:
	air --build.cmd "go build -o bin/signallingserver main.go" \
		--build.full_bin "bin/signallingserver.exe --configFilePath config.yaml"
clean:
	rm bin/*
