package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	// Deye Cloud API
	DeyeBaseURL   string
	DeyeAppID     string
	DeyeAppSecret string
	DeyeEmail     string
	DeyePassword  string

	// Deye Device
	DeyeStationID int64
	DeyeDeviceSN  string

	// Telegram
	TelegramBotToken string
	TelegramUserIDs  []int64

	// Polling
	PollIntervalSec int
}

func LoadConfig() (*Config, error) {
	_ = godotenv.Load()

	var err error

	var stationID int64
	if v := os.Getenv("DEYE_STATION_ID"); v != "" {
		stationID, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid DEYE_STATION_ID: %w", err)
		}
	}

	userIDs, err := parseUserIDs(os.Getenv("TELEGRAM_USER_IDS"))
	if err != nil {
		return nil, fmt.Errorf("invalid TELEGRAM_USER_IDS: %w", err)
	}

	pollInterval := 60
	if v := os.Getenv("POLL_INTERVAL_SEC"); v != "" {
		pollInterval, err = strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid POLL_INTERVAL_SEC: %w", err)
		}
	}

	cfg := &Config{
		DeyeBaseURL:      requiredEnv("DEYE_BASE_URL"),
		DeyeAppID:        requiredEnv("DEYE_APP_ID"),
		DeyeAppSecret:    requiredEnv("DEYE_APP_SECRET"),
		DeyeEmail:        requiredEnv("DEYE_EMAIL"),
		DeyePassword:     requiredEnv("DEYE_PASSWORD"),
		DeyeStationID:    stationID,
		DeyeDeviceSN:     os.Getenv("DEYE_DEVICE_SN"),
		TelegramBotToken: requiredEnv("TELEGRAM_BOT_TOKEN"),
		TelegramUserIDs:  userIDs,
		PollIntervalSec:  pollInterval,
	}

	return cfg, nil
}

func requiredEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required env variable %s is not set", key))
	}
	return v
}

func parseUserIDs(s string) ([]int64, error) {
	parts := strings.Split(s, ",")
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("cannot parse user ID %q: %w", p, err)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no user IDs provided")
	}
	return ids, nil
}
