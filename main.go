package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Honorable-Knights-of-the-Roundtable/roundtable/pkg/signalling"
	"github.com/Honorable-Knights-of-the-Roundtable/signallingserver/config"
	"github.com/google/uuid"
	"github.com/spf13/viper"
)

func handleSignalOffer(w http.ResponseWriter, r *http.Request) {
	requestLogger := slog.Default().WithGroup("request").With(
		"requestUUID", uuid.New().String(),
	)
	requestLogger.Debug("new incoming session offer")

	// TODO: Likely a security risk to read the body... what if the body is very large?
	requestBody, err := io.ReadAll(r.Body)
	if err != nil {
		requestLogger.Error(
			"error while reading request body",
			"err", err,
			"request", r,
		)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var signallingOffer signalling.SignallingOffer
	if err := json.Unmarshal(requestBody, &signallingOffer); err != nil {
		requestLogger.Error(
			"error while decoding new session offer from JSON",
			"err", err,
			"request", r,
			// "requestBody", requestBody,
		)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	requestLogger.With("offerUUID", signallingOffer.OfferUUID.String())
	requestLogger.Info("received signalling offer")

	// --------------------------------------------------------------------------------
	// Forward this offer on to the specified remote endpoint (if possible)

	ctx := context.Background()
	ctx, cancelFunc := context.WithTimeout(ctx, viper.GetDuration("timeout")*time.Second)
	defer cancelFunc()

	// TODO: technically remoteEndpoint is user-defined data,
	// so this should be validated before using for sprintf...?
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("%s/%s", signallingOffer.AnsweringPeerID.PublicIP, signalling.SIGNAL_ENDPOINT),
		bytes.NewReader(requestBody),
	)
	if err != nil {
		requestLogger.Error(
			"error while creating new http request",
			"err", err,
		)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	// If ctx.cancel is called, or ctx timeout is reached, this returns a non-nil error
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		requestLogger.Error(
			"error while posting offer to remote client",
			"err", err,
		)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	requestLogger.Debug("response received from answering client")

	// --------------------------------------------------------------------------------
	// Read response from answering client and forward this back to offering client
	// TODO: Can we avoid reading the answer?

	// TODO: Security of this? What if malicious answeringResponseBody is very large?
	answeringResponseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		requestLogger.Error(
			"error while reading answering request body",
			"err", err,
		)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(answeringResponseBody)

	requestLogger.Info("request fulfilled")
}

func main() {
	configFilePath := flag.String("configFilePath", "config.yaml", "Set the file path to the config file.")
	flag.Parse()

	config.LoadConfig(*configFilePath)
	logFilePointer, err := config.ConfigureDefaultLogger(
		viper.GetString("loglevel"),
		viper.GetString("logfile"),
		slog.HandlerOptions{},
	)
	if err != nil {
		slog.Error("error while configuring default logger", "err", err)
		panic(err)
	}
	if logFilePointer != nil {
		defer logFilePointer.Close()
	}

	// --------------------------------------------------------------------------------

	mux := http.NewServeMux()
	mux.HandleFunc(
		fmt.Sprintf("POST /%s", signalling.SIGNAL_ENDPOINT),
		handleSignalOffer,
	)
	listenAddress := fmt.Sprintf("localhost:%d", viper.GetInt("localport"))
	slog.Debug("starting signalling server listening", "listenAddress", listenAddress)
	if err := http.ListenAndServe(listenAddress, mux); err != nil {
		slog.Error("error during listen and serve", "err", err)
	}
}
