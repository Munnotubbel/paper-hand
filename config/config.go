package config

import (
	"fmt"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
)

// Config speichert die gesamte Anwendungskonfiguration.
type Config struct {
	// Datenbank-Konfigurationen
	RawDBHost       string `envconfig:"POSTGRES_RAW_HOST" default:"localhost"`
	RawDBPort       int    `envconfig:"POSTGRES_RAW_PORT" default:"5432"`
	RawDBUser       string `envconfig:"POSTGRES_RAW_USER" required:"true"`
	RawDBPassword   string `envconfig:"POSTGRES_RAW_PASSWORD" required:"true"`
	RawDBName       string `envconfig:"POSTGRES_RAW_DB" required:"true"`
	RatedDBHost     string `envconfig:"POSTGRES_RATED_HOST" default:"localhost"`
	RatedDBPort     int    `envconfig:"POSTGRES_RATED_PORT" default:"5433"`
	RatedDBUser     string `envconfig:"POSTGRES_RATED_USER" required:"true"`
	RatedDBPassword string `envconfig:"POSTGRES_RATED_PASSWORD" required:"true"`
	RatedDBName     string `envconfig:"POSTGRES_RATED_DB" required:"true"`

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

	// API Security
	APISecretKey string `envconfig:"API_SECRET_KEY"`
}

// RawDSN gibt den DSN-String für die Rohdaten-Datenbank zurück.
func (c *Config) RawDSN() string {
	return fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=disable",
		c.RawDBHost, c.RawDBUser, c.RawDBPassword, c.RawDBName, c.RawDBPort)
}

// RatedDSN gibt den DSN-String für die Bewertungs-Datenbank zurück.
func (c *Config) RatedDSN() string {
	return fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=disable",
		c.RatedDBHost, c.RatedDBUser, c.RatedDBPassword, c.RatedDBName, c.RatedDBPort)
}

// LoadConfig liest die Konfiguration aus den Umgebungsvariablen.
func Load() (*Config, error) {
	_ = godotenv.Load()
	var c Config
	err := envconfig.Process("", &c)
	return &c, err
}
