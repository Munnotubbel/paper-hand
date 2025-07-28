package services

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"paper-hand/config"
	"paper-hand/models"
	"paper-hand/providers"
	"paper-hand/providers/unpaywall"
	"paper-hand/storage"
)

// CustomTransport fügt jeder Anfrage einen User-Agent-Header hinzu.
type CustomTransport struct {
	Transport http.RoundTripper
}

func (t *CustomTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	return t.Transport.RoundTrip(req)
}

// httpClient wird für alle externen HTTP-Anfragen in diesem Service verwendet.
var httpClient = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &CustomTransport{
		Transport: http.DefaultTransport,
	},
}

// FetchService kümmert sich um die Orchestrierung des gesamten Fetch-Prozesses.
type FetchService struct {
	Config           *config.Config
	DB               *gorm.DB
	S3Client         *s3.Client
	Logger           *zap.Logger
	Providers        []providers.Provider
	UnpaywallFetcher *unpaywall.Fetcher
}

// NewFetchService erstellt eine neue Instanz des FetchService.
func NewFetchService(cfg *config.Config, db *gorm.DB, s3 *s3.Client, logger *zap.Logger, providers []providers.Provider) *FetchService {
	return &FetchService{
		Config:           cfg,
		DB:               db,
		S3Client:         s3,
		Logger:           logger,
		Providers:        providers,
		UnpaywallFetcher: unpaywall.NewFetcher(cfg, logger),
	}
}

// RunAllSubstances führt den Fetch-Prozess für alle in der DB definierten Substanzen und Filter aus.
func (f *FetchService) RunForAllSubstances(ctx context.Context) (int, error) {
	var substances []models.Substance
	if err := f.DB.Find(&substances).Error; err != nil {
		f.Logger.Error("Fehler beim Abrufen der Substanzen", zap.Error(err))
		return 0, err
	}

	var filters []models.SearchFilter
	if err := f.DB.Find(&filters).Error; err != nil {
		f.Logger.Error("Fehler beim Abrufen der Suchfilter", zap.Error(err))
		return 0, err
	}

	totalNewPapers := 0
	for _, sub := range substances {
		count, err := f.RunForSubstance(ctx, sub, filters)
		if err != nil {
			f.Logger.Error("Fehler beim Verarbeiten der Substanz", zap.String("substance", sub.Name), zap.Error(err))
			continue
		}
		totalNewPapers += count
	}
	return totalNewPapers, nil
}

// RunForSubstance führt die Suche für eine Substanz mit allen gegebenen Filtern aus.
func (f *FetchService) RunForSubstance(ctx context.Context, sub models.Substance, filters []models.SearchFilter) (int, error) {
	log := f.Logger.With(zap.String("substance", sub.Name))
	log.Info("Starte Fetch-Prozess für Substanz.")

	allPapers := make(map[string]*models.Paper) // De-duplizierung

	for _, filter := range filters {
		finalTerm := fmt.Sprintf("(%s[Title/Abstract]) %s", sub.Name, filter.FilterQuery)
		log.Info("Führe Suche für Filter aus", zap.String("filter_name", filter.Name))

		for _, provider := range f.Providers {
			papers, err := provider.Search(finalTerm)
			if err != nil {
				log.Error("Provider-Suche fehlgeschlagen", zap.String("provider", provider.Name()), zap.Error(err))
				continue
			}
			log.Info("Provider hat Ergebnisse geliefert", zap.String("provider", provider.Name()), zap.Int("count", len(papers)))

			// Ergebnisse de-duplizieren
			for _, paper := range papers {
				paper.StudyDesign = filter.Name // Wichtig: Study Design setzen!
				key := paper.PMID
				if key == "" && paper.DOI != "" { // Fallback auf DOI, falls keine PMID vorhanden
					key = paper.DOI
				}

				if key != "" {
					if _, exists := allPapers[key]; !exists {
						allPapers[key] = paper
					}
				}
			}
		}
	}

	// Konvertiere Map zurück in eine Slice
	var uniquePapers []*models.Paper
	for _, paper := range allPapers {
		uniquePapers = append(uniquePapers, paper)
	}

	log.Info("Suche bei allen Providern abgeschlossen", zap.Int("total_unique_papers", len(uniquePapers)))

	// 2. Details für jede ID parallel verarbeiten
	var wg sync.WaitGroup
	var newPapersCount int
	semaphore := make(chan struct{}, 5) // Limit auf 5 parallele Verarbeitungen

	for _, paper := range uniquePapers {
		wg.Add(1)
		semaphore <- struct{}{}

		go func(paper *models.Paper) {
			defer wg.Done()
			defer func() { <-semaphore }()

			// Details holen
			// provider := pubmed.NewFetcher(f.Config, f.Logger) // This line is no longer needed
			// paper, err := provider.FetchPaperDetails(pmid) // This line is no longer needed
			// if err != nil { // This line is no longer needed
			// 	log.Error("Konnte Paper-Details nicht abrufen", zap.String("pmid", pmid), zap.Error(err)) // This line is no longer needed
			// 	return // This line is no longer needed
			// } // This line is no longer needed
			paper.Substance = sub.Name // Setze Substanz für die Verarbeitung

			// ERST JETZT: Duplikatsprüfung mit vollen Paper-Daten
			var existing models.Paper
			query := f.DB.Where("pmid = ?", paper.PMID)
			if paper.DOI != "" {
				query = query.Or("doi = ?", paper.DOI)
			}
			if err := query.First(&existing).Error; err == nil && existing.CloudStored {
				log.Debug("Paper bereits vorhanden (PMID oder DOI) und in S3 gespeichert, wird übersprungen.",
					zap.String("pmid", paper.PMID), zap.String("doi", paper.DOI))
				return
			}

			// Paper verarbeiten (Download & Upload)
			if f.processPaper(ctx, paper) {
				newPapersCount++
			}
		}(paper)
	}

	wg.Wait()
	log.Info("Verarbeitung für Substanz abgeschlossen", zap.Int("new_papers_found", newPapersCount))
	return newPapersCount, nil
}

// processPaper verarbeitet ein einzelnes Paper-Objekt.
func (f *FetchService) processPaper(ctx context.Context, paper *models.Paper) bool {
	log := f.Logger.With(zap.String("pmid", paper.PMID), zap.String("doi", paper.DOI))

	// Zentraler Unpaywall-Fallback, falls kein Download-Link vom Provider kam
	if paper.DownloadLink == "" && paper.DOI != "" {
		log.Info("Kein direkter Link vom Provider, versuche Unpaywall-Fallback.", zap.String("doi", paper.DOI))
		link, err := f.UnpaywallFetcher.GetPDFLink(paper.DOI)
		if err != nil {
			log.Warn("Unpaywall-Fallback fehlgeschlagen", zap.Error(err))
		} else if link != "" {
			log.Info("Erfolgreich Download-Link über Unpaywall-Fallback gefunden.", zap.String("link", link))
			paper.DownloadLink = link
		}
	}

	// Download-Logik
	if paper.DownloadLink == "" {
		log.Warn("Kein Download-Link vorhanden, Verarbeitung hier beendet.")
		paper.NoPDFFound = true
		f.DB.Save(paper)
		return true // Zählt als "neu" verarbeitet, da wir es versucht haben
	}

	log.Info("Starte Download", zap.String("url", paper.DownloadLink))
	data, foundPDF, err := f.downloadResource(paper.DownloadLink)
	if err != nil {
		log.Warn("Download fehlgeschlagen", zap.Error(err), zap.String("url", paper.DownloadLink))
		paper.NoPDFFound = true
		f.DB.Save(paper)
		return true
	}
	if !foundPDF {
		log.Warn("Ressource heruntergeladen, aber keine PDF-Datei darin gefunden.", zap.String("url", paper.DownloadLink))
		paper.NoPDFFound = true
		f.DB.Save(paper)
		return true
	}

	// S3 Upload
	key := paper.PMID + ".pdf"
	log.Info("Lade PDF nach S3 hoch", zap.String("key", key))
	s3link, err := storage.UploadFile(f.S3Client, f.Config.StratoS3Bucket, key, data, f.Config)
	if err != nil {
		log.Error("S3-Upload fehlgeschlagen", zap.Error(err))
		// Wir speichern trotzdem den Rest
	} else {
		paper.S3Link = s3link
		paper.CloudStored = true
		log.Info("PDF erfolgreich nach S3 hochgeladen", zap.String("s3_link", s3link))
	}
	paper.NoPDFFound = false
	paper.DownloadDate = timePtr(time.Now())
	f.DB.Save(paper)

	log.Info("Paper erfolgreich verarbeitet.")
	return true
}

// downloadResource lädt eine Ressource herunter.
func (f *FetchService) downloadResource(link string) ([]byte, bool, error) {
	resp, err := httpClient.Get(link)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("bad status: %s", resp.Status)
	}

	// Direkte PDF
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(strings.ToLower(contentType), "pdf") || strings.HasSuffix(strings.ToLower(link), ".pdf") {
		f.Logger.Debug("Direkte PDF erkannt (Content-Type oder Suffix).")
		data, err := io.ReadAll(resp.Body)
		return data, true, err
	}

	// Tar.gz-Archiv
	if strings.HasSuffix(strings.ToLower(link), ".tar.gz") || strings.HasSuffix(strings.ToLower(link), ".tgz") {
		f.Logger.Debug("Tar.gz-Archiv erkannt, starte Extraktion.")
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, false, err
		}
		defer gz.Close()

		tr := tar.NewReader(gz)
		for {
			header, err := tr.Next()
			if err == io.EOF {
				break // Ende des Archivs
			}
			if err != nil {
				return nil, false, err
			}
			if header.Typeflag == tar.TypeReg && strings.HasSuffix(strings.ToLower(header.Name), ".pdf") {
				f.Logger.Info("PDF in Tar.gz gefunden", zap.String("filename", header.Name))
				pdfBytes, err := io.ReadAll(tr)
				return pdfBytes, true, err
			}
		}
	}

	f.Logger.Warn("Konnte Ressourcentyp nicht bestimmen oder keine PDF gefunden.", zap.String("content_type", contentType))
	return nil, false, nil // Kein Fehler, aber auch keine PDF gefunden
}

// timePtr gibt einen Pointer auf eine time.Time zurück.
func timePtr(t time.Time) *time.Time {
	return &t
}
