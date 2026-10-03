// Package config loads the server configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all server configuration.
type Config struct {
	ListenAddr        string
	DBURL             string
	S3Endpoint        string
	S3AccessKey       string
	S3SecretKey       string
	S3Bucket          string
	S3UseSSL          bool
	TeamusersURL      string
	TeamusersAud      string
	TeamusersSvcToken string
	TeamusersClientID string
	TeamusersSecret   string
	FilehouseURL      string
	FilehouseBucket   string
	DevMode           bool
}

// Load reads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:        envOrDefault("WEBCAM_LISTEN_ADDR", ":8080"),
		DBURL:             os.Getenv("WEBCAM_DB_URL"),
		S3Endpoint:        envOrDefault("WEBCAM_S3_ENDPOINT", "localhost:9000"),
		S3AccessKey:       os.Getenv("WEBCAM_S3_ACCESS_KEY"),
		S3SecretKey:       os.Getenv("WEBCAM_S3_SECRET_KEY"),
		S3Bucket:          envOrDefault("WEBCAM_S3_BUCKET", "webcam-streams"),
		TeamusersURL:      os.Getenv("WEBCAM_TEAMUSERS_URL"),
		TeamusersAud:      envOrDefault("WEBCAM_TEAMUSERS_AUD", "teamusers"),
		TeamusersSvcToken: os.Getenv("WEBCAM_TEAMUSERS_SVC_TOKEN"),
		TeamusersClientID: os.Getenv("WEBCAM_TEAMUSERS_CLIENT_ID"),
		TeamusersSecret:   os.Getenv("WEBCAM_TEAMUSERS_CLIENT_SECRET"),
		FilehouseURL:      os.Getenv("WEBCAM_FILEHOUSE_URL"),
		FilehouseBucket:   envOrDefault("WEBCAM_FILEHOUSE_BUCKET", "webcam-segments"),
	}

	var err error
	cfg.DevMode, err = strconv.ParseBool(envOrDefault("WEBCAM_DEV", "false"))
	if err != nil {
		return nil, fmt.Errorf("WEBCAM_DEV: %w", err)
	}

	sslStr := envOrDefault("WEBCAM_S3_USE_SSL", "false")
	cfg.S3UseSSL, err = strconv.ParseBool(sslStr)
	if err != nil {
		return nil, fmt.Errorf("WEBCAM_S3_USE_SSL: %w", err)
	}

	if cfg.DBURL == "" {
		return nil, fmt.Errorf("WEBCAM_DB_URL is required")
	}
	if !cfg.DevMode && cfg.TeamusersURL == "" {
		return nil, fmt.Errorf("WEBCAM_TEAMUSERS_URL is required (or set WEBCAM_DEV=true)")
	}

	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
