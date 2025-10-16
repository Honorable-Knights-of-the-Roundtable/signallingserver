# Roundtable Signalling Server

The signalling server for Roundtable. This project does not handle VoIP traffic itself, nor does it provide TURN services (see [coturn](https://github.com/coturn/coturn) for that.) This project just allows for signalling between peers to establish a peer-to-peer connection, punching through NAT so peers off of the public internet may start communications.

A very basic signalling server to be hosted publicly. This server forwards SDP offers from one client to another, and responds with the corresponding answer, allowing for peer to peer connections to be made. Ideally this server is hosted on a publicly available IP address that is behind some kind of DDOS protection (e.g. Cloudflare) to avoid malicious agents using this server in an amplification attack on unsuspecting remote clients.

Beware, if hiding this server behind a port-forwarder/reverse proxy, to give your clients the *publicly available* address for the signalling server, including the external port number!!

### Building this project

Run `make build` or `go build -o bin/signallingserver ./main.go` to build the signalling server. The resulting binary is built to `bin/signallingserver`

Alternatively, for development, use `make dev` to hot reload the binary.

To run the server, set the `config.yaml` configuration file (see below) and run `bin/signallingserver --configFilePath config.yaml`. The server will start listening on a port determined by the config file. In production, be sure this port is publicly accessible.

### Config

| Key | Datatype    | Default Value | Description   |
| --- | ---         | ---           | ---           |
| loglevel | String Enum ("none", "error", "warn", "info", "debug") | "info" | The level at which logs are recorded. None disables logging. |
| logfile | String | nil | The filepath to write logs to. If left unset or empty, logs are sent to `stdout`. The file is truncated before logging begins. If the file cannot be opened for writing, the program panics. |
| localport | int | 1066 | Defines the local port number to bind to for listening to incoming peer connections from the signalling server. |
| timeout | int | 30 | Defines the time (in seconds) to wait for a request before timing out. |

# Dependencies

### Development Dependencies

- `Make` is used to help build aspects of this project.

##### Optional

- [`air`](https://github.com/air-verse/air), a hot-reload tool, is used in some development commands within the Makefile, but this is optional.