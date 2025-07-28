package config

import (
	"fmt"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
)

// Config enthält alle Konfigurationsparameter aus Umgebungsvariablen.
type Config struct {
	DBHost     string `envconfig:"DB_HOST" required:"true"`
	DBPort     int    `envconfig:"DB_PORT" default:"5432"`
	DBUser     string `envconfig:"DB_USER" required:"true"`
	DBPassword string `envconfig:"DB_PASSWORD" required:"true"`
	DBName     string `envconfig:"DB_NAME" required:"true"`

	HTTPPort string `envconfig:"HTTP_PORT" default:"4242"`

	PubMedBaseURL          string `envconfig:"PUBMED_BASE_URL" default:"https://eutils.ncbi.nlm.nih.gov/entrez/eutils"`
	PubMedAPIKey           string `envconfig:"PUBMED_API_KEY"`
	PubMedEmail            string `envconfig:"PUBMED_EMAIL"`
	PubMedTool             string `envconfig:"PUBMED_TOOL" default:"paper-hand-fetcher"`
	PubMedFreeFullTextOnly bool   `envconfig:"PUBMED_FREE_FULL_TEXT_ONLY" default:"false"`
	PubMedMaxPages         int    `envconfig:"PUBMED_MAX_PAGES" default:"50"`

	CronSchedule string `envconfig:"CRON_SCHEDULE" default:"0 0 * * *"`
	// Unpaywall-API für freie Volltexte fallback
	UnpaywallBaseURL string `envconfig:"UNPAYWALL_BASE_URL" default:"https://api.unpaywall.org/v2"`
	UnpaywallEmail   string `envconfig:"UNPAYWALL_EMAIL" required:"true"`

	DebugMaxRecords int `envconfig:"DEBUG_MAX_RECORDS" default:"30"`

	StratoS3Key    string `envconfig:"STRATO_S3_KEY" required:"true"`
	StratoS3Secret string `envconfig:"STRATO_S3_SECRET" required:"true"`
	StratoS3URL    string `envconfig:"STRATO_S3_URL" required:"true"`
	StratoS3Region string `envconfig:"STRATO_S3_REGION" required:"true"`
	StratoS3Bucket string `envconfig:"STRATO_S3_BUCKET" required:"true"`

	// Provider-Konfiguration
	EnabledProviders string `envconfig:"ENABLED_PROVIDERS" default:"pubmed,europepmc"`
}

// DSN gibt den Data Source Name für die PostgreSQL-Verbindung zurück.
func (c *Config) DSN() string {
	return fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=disable",
		c.DBHost, c.DBUser, c.DBPassword, c.DBName, c.DBPort)
}

// Load lädt die Konfiguration aus den Umgebungsvariablen.
func Load() (*Config, error) {
	_ = godotenv.Load()
	var c Config
	err := envconfig.Process("", &c)
	return &c, err
}
