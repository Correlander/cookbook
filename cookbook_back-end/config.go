package main

import (
	"errors"
	"os"
	"strings"
)

type config struct {
	address  string
	database string
}

func loadConfig() (config, error) {
	address := strings.TrimSpace(os.Getenv("COOKBOOK_ADDR"))
	if address == "" {
		address = "127.0.0.1:8000"
	}
	if !strings.Contains(address, ":") {
		return config{}, errors.New("COOKBOOK_ADDR must be a host:port address")
	}
	database := strings.TrimSpace(os.Getenv("COOKBOOK_DATABASE"))
	if database == "" {
		database = "cookbook.db"
	}
	return config{address: address, database: database}, nil
}
