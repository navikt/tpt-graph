package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port                string
	DatabaseURL         string
	Neo4jURI            string
	Neo4jUser           string
	Neo4jPassword       string
	Neo4jDatabase       string
	GoogleCloudProject  string
	GoogleCloudLocation string
	VertexModel         string
	QueryTimeout        time.Duration
	MaxRows             int
	MaxResultBytes      int
}

func Load() (Config, error) {
	var config Config
	var err error
	if config.DatabaseURL, err = required("DB_URL"); err != nil {
		return Config{}, err
	}
	if config.Neo4jURI, err = required("NEO4J_URI"); err != nil {
		return Config{}, err
	}
	if config.Neo4jUser, err = required("NEO4J_USER"); err != nil {
		return Config{}, err
	}
	if config.Neo4jPassword, err = required("NEO4J_PASSWORD"); err != nil {
		return Config{}, err
	}
	config.Port = get("PORT", "8080")
	config.Neo4jDatabase = get("NEO4J_DATABASE", "neo4j")
	config.GoogleCloudProject = os.Getenv("GOOGLE_CLOUD_PROJECT")
	config.GoogleCloudLocation = get("GOOGLE_CLOUD_LOCATION", "eu")
	config.VertexModel = get("VERTEX_MODEL", "gemini-3.8-flash")
	config.QueryTimeout, err = seconds("NEO4J_QUERY_TIMEOUT_SECONDS", 10)
	if err != nil {
		return Config{}, err
	}
	config.MaxRows, err = positiveInt("NEO4J_MAX_RESULT_ROWS", 200)
	if err != nil {
		return Config{}, err
	}
	config.MaxResultBytes, err = positiveInt("NEO4J_MAX_RESULT_BYTES", 1<<20)
	if err != nil {
		return Config{}, err
	}
	return config, nil
}

func required(key string) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", fmt.Errorf("required environment variable %s is not set", key)
	}
	return value, nil
}

func get(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func positiveInt(key string, fallback int) (int, error) {
	value := get(key, strconv.Itoa(fallback))
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func seconds(key string, fallback int) (time.Duration, error) {
	value := get(key, strconv.Itoa(fallback))
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive number of seconds", key)
	}
	return time.Duration(parsed) * time.Second, nil
}
