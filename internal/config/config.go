package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultWebAddress    = ":8080"
	DefaultLocalSendPort = 53317
)

type Config struct {
	Alias            string
	WebAddress       string
	LocalSendPort    int
	DataDirectory    string
	SendDirectory    string
	ReceiveDirectory string
}

type LookupEnv func(string) (string, bool)

func Parse(args []string, lookupEnv LookupEnv) (Config, error) {
	defaults, err := defaultsFromEnvironment(lookupEnv)
	if err != nil {
		return Config{}, err
	}

	flags := flag.NewFlagSet("gosend", flag.ContinueOnError)
	cfg := defaults
	flags.StringVar(&cfg.Alias, "alias", defaults.Alias, "device name advertised on the local network")
	flags.StringVar(&cfg.WebAddress, "web-address", defaults.WebAddress, "Web UI listen address")
	flags.IntVar(&cfg.LocalSendPort, "localsend-port", defaults.LocalSendPort, "LocalSend TCP and UDP port")
	flags.StringVar(&cfg.DataDirectory, "data-dir", defaults.DataDirectory, "persistent application data directory")
	flags.StringVar(&cfg.SendDirectory, "send-dir", defaults.SendDirectory, "directory containing files available to send")
	flags.StringVar(&cfg.ReceiveDirectory, "receive-dir", defaults.ReceiveDirectory, "directory for received files")

	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	return normalizeAndValidate(cfg)
}

func defaultsFromEnvironment(lookupEnv LookupEnv) (Config, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}

	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "GoSend"
	}

	port := DefaultLocalSendPort
	if raw, ok := lookupEnv("GOSEND_LOCALSEND_PORT"); ok {
		port, err = strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("GOSEND_LOCALSEND_PORT must be an integer: %w", err)
		}
	}

	dataDirectory := envOr(lookupEnv, "GOSEND_DATA_DIR", "./data")

	return Config{
		Alias:            envOr(lookupEnv, "GOSEND_ALIAS", hostname),
		WebAddress:       envOr(lookupEnv, "GOSEND_WEB_ADDRESS", DefaultWebAddress),
		LocalSendPort:    port,
		DataDirectory:    dataDirectory,
		SendDirectory:    envOr(lookupEnv, "GOSEND_SEND_DIR", ""),
		ReceiveDirectory: envOr(lookupEnv, "GOSEND_RECEIVE_DIR", ""),
	}, nil
}

func normalizeAndValidate(cfg Config) (Config, error) {
	cfg.Alias = strings.TrimSpace(cfg.Alias)
	cfg.WebAddress = strings.TrimSpace(cfg.WebAddress)
	if cfg.Alias == "" {
		return Config{}, errors.New("alias must not be empty")
	}
	if cfg.WebAddress == "" {
		return Config{}, errors.New("web address must not be empty")
	}
	if cfg.LocalSendPort < 1 || cfg.LocalSendPort > 65535 {
		return Config{}, errors.New("LocalSend port must be between 1 and 65535")
	}

	var err error
	cfg.DataDirectory, err = absolutePath(cfg.DataDirectory)
	if err != nil {
		return Config{}, fmt.Errorf("resolve data directory: %w", err)
	}
	if strings.TrimSpace(cfg.SendDirectory) == "" {
		cfg.SendDirectory = filepath.Join(cfg.DataDirectory, "send")
	}
	if strings.TrimSpace(cfg.ReceiveDirectory) == "" {
		cfg.ReceiveDirectory = filepath.Join(cfg.DataDirectory, "receive")
	}
	cfg.SendDirectory, err = absolutePath(cfg.SendDirectory)
	if err != nil {
		return Config{}, fmt.Errorf("resolve send directory: %w", err)
	}
	cfg.ReceiveDirectory, err = absolutePath(cfg.ReceiveDirectory)
	if err != nil {
		return Config{}, fmt.Errorf("resolve receive directory: %w", err)
	}
	if samePath(cfg.SendDirectory, cfg.ReceiveDirectory) {
		return Config{}, errors.New("send and receive directories must be different")
	}

	return cfg, nil
}

func envOr(lookupEnv LookupEnv, name, fallback string) string {
	if value, ok := lookupEnv(name); ok {
		return value
	}
	return fallback
}

func absolutePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path must not be empty")
	}
	return filepath.Abs(filepath.Clean(path))
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
