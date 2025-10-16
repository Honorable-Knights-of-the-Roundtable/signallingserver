package config

import (
	"errors"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/viper"
)

func LoadConfig(configFilePath string) {
	viper.SetDefault("loglevel", "info")
	viper.SetDefault("logfile", "")
	viper.SetDefault("localport", 1066)
	viper.SetDefault("timeout", 30)

	viper.SetConfigFile(configFilePath)
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			slog.Info("no config file found", "configFilePath", configFilePath)
		} else {
			slog.Error("error during config read", "err", err)
			panic(err)
		}
	}
}

// Configure the slog logger with a specific log level and potential output file.
//
// Valid log levels are "none", "error", "warn", "info", "debug". Any other value returns an error.
// logFile may either specify a file path (an error is returned if the path cannot be opened) or none,
// in which case the logger points to stdout.
//
// Returns the os.File pointer that slog writes to, so it may be gracefully shut:
// ```
// logFilePointer := config.ConfigureLogger()
//
//	if logFilePointer != nil{
//		defer logFilePointer.Close()
//	}
//
// ```
func ConfigureDefaultLogger(logLevel string, logFile string, loggerOptions slog.HandlerOptions) (*os.File, error) {

	switch logLevel {
	case "none":
		// No logging is required, disable the logger and return
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return nil, nil
	case "error":
		loggerOptions.Level = slog.LevelError
	case "warn":
		loggerOptions.Level = slog.LevelWarn
	case "info":
		loggerOptions.Level = slog.LevelInfo
	case "debug":
		loggerOptions.Level = slog.LevelDebug
	default:
		return nil, errors.New("unexpected log level")
	}

	// --------------------------------------------------------------------------------

	var logFilePointer *os.File
	var slogHandler slog.Handler
	if logFile == "" {
		logFilePointer = nil
		slogHandler = slog.NewTextHandler(os.Stdout, &loggerOptions)
	} else {
		logFilePointer, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return nil, err
		}
		slogHandler = slog.NewJSONHandler(logFilePointer, &loggerOptions)
	}

	// --------------------------------------------------------------------------------

	slog.SetDefault(slog.New(slogHandler))
	return logFilePointer, nil
}
