package unpaywall

import (
	"encoding/json"
	"fmt"
	"net/http"
	"paper-hand/config"
	"time"

	"go.uber.org/zap"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Response repräsentiert die JSON-Antwort der Unpaywall-API.
type Response struct {
	BestOALocation struct {
		URLForPDF string `json:"url_for_pdf"`
	} `json:"best_oa_location"`
}

// Fetcher kapselt die Logik für Unpaywall.
type Fetcher struct {
	Config *config.Config
	Logger *zap.Logger
}

// NewFetcher erstellt einen neuen Unpaywall-Fetcher.
func NewFetcher(cfg *config.Config, logger *zap.Logger) *Fetcher {
	return &Fetcher{Config: cfg, Logger: logger}
}

// GetPDFLink holt einen freien PDF-Link via Unpaywall anhand der DOI.
func (f *Fetcher) GetPDFLink(doi string) (string, error) {
	if f.Config.UnpaywallEmail == "" {
		return "", fmt.Errorf("unpaywall email ist nicht konfiguriert")
	}

	url := fmt.Sprintf("%s/%s?email=%s", f.Config.UnpaywallBaseURL, doi, f.Config.UnpaywallEmail)
	log := f.Logger.With(zap.String("doi", doi), zap.String("url", url))
	log.Debug("Rufe Unpaywall API auf.")

	resp, err := httpClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unpaywall request failed with status: %d", resp.StatusCode)
	}

	var ur Response
	if err := json.NewDecoder(resp.Body).Decode(&ur); err != nil {
		return "", err
	}

	if ur.BestOALocation.URLForPDF != "" {
		log.Info("PDF-Link über Unpaywall gefunden.")
		return ur.BestOALocation.URLForPDF, nil
	}

	log.Debug("Kein PDF-Link in Unpaywall-Antwort gefunden.")
	return "", nil
}
