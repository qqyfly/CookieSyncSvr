package config

import (
	"flag"
	"os"
	"strings"
)

// Config holds the runtime configuration for CookieSyncSvr.
type Config struct {
	ListenAddr     string
	DBDriver       string // "sqlite" or "postgres"
	DatabaseURL    string // SQLite file path or PostgreSQL connection string
	APIKey         string // Optional Bearer / X-API-Key token
	EncryptionKey  string // Optional 32-byte key (or passphrase hashed to 32 bytes) for AES-GCM at rest
	AllowedOrigins string // Comma-separated allowed CORS origins (default "*")
	MQTTBrokerURL  string // Optional MQTT broker URL, e.g. "tcp://127.0.0.1:1883"
	MQTTTopic      string // MQTT topic prefix, default "cookiesync/updated"
	MQTTUsername   string
	MQTTPassword   string
}

// Load reads configuration from CLI flags and environment variables.
func Load() *Config {
	cfg := &Config{}

	flag.StringVar(&cfg.ListenAddr, "listen", getEnv("LISTEN_ADDR", ":8080"), "HTTP listen address (env: LISTEN_ADDR)")
	flag.StringVar(&cfg.DBDriver, "db-driver", getEnv("DB_DRIVER", "sqlite"), "Database driver: sqlite or postgres (env: DB_DRIVER)")
	flag.StringVar(&cfg.DatabaseURL, "db-url", getEnv("DATABASE_URL", "./cookiesync.db"), "Database URL or SQLite file path (env: DATABASE_URL)")
	flag.StringVar(&cfg.APIKey, "api-key", getEnv("API_KEY", ""), "API Key for authenticating requests (env: API_KEY)")
	flag.StringVar(&cfg.EncryptionKey, "encryption-key", getEnv("ENCRYPTION_KEY", ""), "Optional secret key for AES-256-GCM encryption at rest (env: ENCRYPTION_KEY)")
	flag.StringVar(&cfg.AllowedOrigins, "cors-origins", getEnv("CORS_ALLOWED_ORIGINS", "*"), "Comma-separated allowed CORS origins (env: CORS_ALLOWED_ORIGINS)")
	flag.StringVar(&cfg.MQTTBrokerURL, "mqtt-broker", getEnv("MQTT_BROKER_URL", ""), "Optional MQTT broker URL, e.g. tcp://localhost:1883 (env: MQTT_BROKER_URL)")
	flag.StringVar(&cfg.MQTTTopic, "mqtt-topic", getEnv("MQTT_TOPIC", "cookiesync/updated"), "MQTT notification topic (env: MQTT_TOPIC)")
	flag.StringVar(&cfg.MQTTUsername, "mqtt-user", getEnv("MQTT_USERNAME", ""), "Optional MQTT username (env: MQTT_USERNAME)")
	flag.StringVar(&cfg.MQTTPassword, "mqtt-pass", getEnv("MQTT_PASSWORD", ""), "Optional MQTT password (env: MQTT_PASSWORD)")
	flag.Parse()

	cfg.DBDriver = strings.ToLower(strings.TrimSpace(cfg.DBDriver))
	return cfg
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}
