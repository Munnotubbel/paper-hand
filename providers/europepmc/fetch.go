package europepmc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"paper-hand/config"
	"paper-hand/models"
	"strings"
	"time"

	"go.uber.org/zap"
)

const baseURL = "https://www.ebi.ac.uk/europepmc/webservices/rest/search"

var httpClient = &http.Client{Timeout: 60 * time.Second}

// Fetcher implementiert das Provider-Interface für Europe PMC.
type Fetcher struct {
	Config *config.Config
	Logger *zap.Logger
}

// NewFetcher erstellt einen neuen Europe PMC Fetcher.
func NewFetcher(cfg *config.Config, logger *zap.Logger) *Fetcher {
	return &Fetcher{Config: cfg, Logger: logger}
}

// Name gibt den Namen des Providers zurück.
func (f *Fetcher) Name() string {
	return "europepmc"
}

// Search führt die Suche auf Europe PMC aus.
func (f *Fetcher) Search(term string) ([]*models.Paper, error) {
	log := f.Logger.With(zap.String("term", term))
	log.Info("Starte Suche auf Europe PMC.")

	// Wir fügen den Open Access Filter direkt zur Query hinzu, falls gewünscht.
	query := term
	if f.Config.PubMedFreeFullTextOnly { // Wir nutzen dieselbe Variable, um das Verhalten zu steuern
		query += " OPEN_ACCESS:\"y\""
	}

	searchURL := fmt.Sprintf("%s?query=%s&format=json&resultType=core", baseURL, url.QueryEscape(query))
	log.Debug("Rufe Europe PMC API auf", zap.String("url", searchURL))

	resp, err := httpClient.Get(searchURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var searchResponse SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResponse); err != nil {
		return nil, err
	}

	var papers []*models.Paper
	for _, article := range searchResponse.ResultList.Result {
		papers = append(papers, mapArticleToModel(&article))
	}

	log.Info("Suche auf Europe PMC abgeschlossen", zap.Int("found_papers", len(papers)))
	return papers, nil
}

// mapArticleToModel konvertiert ein Europe PMC Article-Objekt in unser internes Paper-Modell.
func mapArticleToModel(article *Article) *models.Paper {
	paper := &models.Paper{
		PMID:            article.PMID,
		DOI:             article.DOI,
		Title:           article.Title,
		Abstract:        article.AbstractText,
		Authors:         article.AuthorString,
		Substance:       "", // Wird später vom FetchService gefüllt
		PublicURL:       fmt.Sprintf("https://europepmc.org/article/MED/%s", article.PMID),
		StudyDate:       parseEuroDate(article.FirstPublicationDate),
		PublicationType: "Journal Article", // Standardwert
	}

	// Finde den besten PDF-Link
	for _, url := range article.FullTextURLList.FullTextURL {
		if url.DocumentStyle == "pdf" && url.AvailabilityCode == "OA" {
			paper.DownloadLink = url.URL
			break
		}
	}

	// Bestimme den Publikationstyp (z.B. Preprint)
	for _, pubType := range article.PubTypeList.PubType {
		if strings.ToLower(pubType) == "preprint" {
			paper.PublicationType = "Preprint"
			break
		}
	}

	return paper
}
