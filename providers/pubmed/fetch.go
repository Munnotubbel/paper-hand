package pubmed

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"paper-hand/config"
	"paper-hand/models"
	"regexp"
	"strings"
	"time"

	"sync"

	"go.uber.org/zap"
)

var (
	httpClient = &http.Client{Timeout: 60 * time.Second}
	pdfRegex   = regexp.MustCompile(`href="([^"]+\.pdf)"`)
	tarRegex   = regexp.MustCompile(`href="([^"]+\.tar\.gz)"`)
)

// Fetcher ist eine Struktur, die die Logik zur Interaktion mit PubMed kapselt.
type Fetcher struct {
	Config *config.Config
	Logger *zap.Logger
}

// NewFetcher erstellt eine neue Instanz des PubMed-Fetchers.
func NewFetcher(cfg *config.Config, logger *zap.Logger) *Fetcher {
	return &Fetcher{Config: cfg, Logger: logger}
}

// Name gibt den Namen des Providers zurück.
func (f *Fetcher) Name() string {
	return "pubmed"
}

// Search führt eine vollständige Suche auf PubMed durch: holt IDs und dann die Details für jede ID.
func (f *Fetcher) Search(term string) ([]*models.Paper, error) {
	ids, err := f.searchIDs(term)
	if err != nil {
		return nil, fmt.Errorf("fehler bei der PubMed ID-Suche: %w", err)
	}

	var papers []*models.Paper
	var wg sync.WaitGroup
	var mu sync.Mutex
	semaphore := make(chan struct{}, 5) // Parallele Abfragen limitieren

	for _, pmid := range ids {
		wg.Add(1)
		semaphore <- struct{}{}

		go func(pmid string) {
			defer wg.Done()
			defer func() { <-semaphore }()

			paper, err := f.fetchPaperDetails(pmid)
			if err != nil {
				f.Logger.Warn("Konnte Details für PMID nicht abrufen", zap.String("pmid", pmid), zap.Error(err))
				return
			}
			mu.Lock()
			papers = append(papers, paper)
			mu.Unlock()
		}(pmid)
	}

	wg.Wait()
	return papers, nil
}

// searchIDs führt eine ESearch-Abfrage durch und gibt eine Liste von PMIDs zurück.
func (f *Fetcher) searchIDs(term string) ([]string, error) {
	log := f.Logger.With(zap.String("term", term))
	log.Info("Starte PubMed ESearch für IDs.")

	query := term
	if f.Config.PubMedFreeFullTextOnly {
		query += " AND free full text[filter]"
	}

	var allIDs []string
	for offset := 0; ; offset += f.Config.PubMedMaxPages {
		searchURL := f.buildEsearchURL(query, f.Config.PubMedMaxPages, offset)
		log.Debug("Rufe ESearch-URL auf", zap.String("url", searchURL))

		resp, err := httpClient.Get(searchURL)
		if err != nil {
			log.Error("ESearch-Anfrage fehlgeschlagen", zap.Error(err))
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := ioutil.ReadAll(resp.Body)
			log.Error("ESearch-API hat nicht-200-Status zurückgegeben",
				zap.Int("status", resp.StatusCode),
				zap.String("body", string(body)))
			return nil, fmt.Errorf("esearch failed: status %d", resp.StatusCode)
		}

		var esearchResp ESearchResponse
		if err := json.NewDecoder(resp.Body).Decode(&esearchResp); err != nil {
			log.Error("Fehler beim Parsen der ESearch-JSON-Antwort", zap.Error(err))
			return nil, err
		}

		ids := esearchResp.ESearchResult.IdList
		if len(ids) == 0 {
			break
		}
		allIDs = append(allIDs, ids...)
		log.Debug("Erfolgreich IDs von ESearch erhalten", zap.Int("count", len(ids)), zap.Int("offset", offset))

		if len(ids) < f.Config.PubMedMaxPages {
			break
		}
	}
	log.Info("PubMed ESearch abgeschlossen", zap.Int("total_ids", len(allIDs)))
	return allIDs, nil
}

// fetchPaperDetails holt die vollständigen Metadaten und den besten Download-Link für eine einzelne PMID.
func (f *Fetcher) fetchPaperDetails(pmid string) (*models.Paper, error) {
	log := f.Logger.With(zap.String("pmid", pmid))
	log.Info("Hole Paper-Details für PMID.")

	// 1. Metadaten via EFetch holen
	paper, err := f.fetchMetadata(pmid)
	if err != nil {
		log.Error("Fehler beim Holen der Metadaten via EFetch", zap.Error(err))
		return nil, err
	}

	// 2. PMCID via ID Converter holen
	pmcID, err := f.getPmcIDFromConverter(pmid)
	if err != nil {
		log.Warn("Fehler beim Holen der PMCID", zap.Error(err))
	}

	// 3. Download-Link via PMC OA holen
	if pmcID != "" {
		log.Debug("PMCID gefunden, versuche PMC OA Feed", zap.String("pmcid", pmcID))
		link, err := f.getLinkFromOA(pmcID)
		if err == nil && link != "" {
			paper.DownloadLink = link
			log.Info("Download-Link über PMC OA Feed gefunden", zap.String("link", link))
		} else if err != nil {
			log.Warn("Fehler beim Abruf des PMC OA Feeds", zap.Error(err))
		}
	}

	// Unpaywall-Fallback wird jetzt zentral im FetchService gehandhabt.
	if paper.DownloadLink == "" {
		log.Debug("Kein direkter Download-Link über PubMed-Quellen gefunden. Übergabe an FetchService für weitere Fallbacks.")
	}

	return paper, nil
}

// fetchMetadata holt Metadaten für eine einzelne PMID via EFetch.
func (f *Fetcher) fetchMetadata(pmid string) (*models.Paper, error) {
	efetchURL := fmt.Sprintf("%s/efetch.fcgi?db=pubmed&id=%s&retmode=xml&api_key=%s",
		f.Config.PubMedBaseURL, pmid, f.Config.PubMedAPIKey)
	f.Logger.Debug("Rufe EFetch-URL für Metadaten auf", zap.String("url", efetchURL))

	resp, err := httpClient.Get(efetchURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("efetch metadata failed: status %d", resp.StatusCode)
	}

	var articleSet PubmedArticleSet
	if err := xml.NewDecoder(resp.Body).Decode(&articleSet); err != nil {
		return nil, err
	}

	if len(articleSet.PubmedArticle) == 0 {
		return nil, fmt.Errorf("kein PubmedArticle in EFetch-Antwort für PMID %s gefunden", pmid)
	}

	return mapArticleToModel(&articleSet.PubmedArticle[0]), nil
}

// getPmcIDFromConverter holt die PMCID über den PMC ID Converter.
func (f *Fetcher) getPmcIDFromConverter(pmid string) (string, error) {
	url := fmt.Sprintf("https://www.ncbi.nlm.nih.gov/pmc/utils/idconv/v1.0/?ids=%s&format=json", pmid)
	f.Logger.Debug("Rufe ID Converter URL auf", zap.String("url", url))

	resp, err := httpClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var convResponse IDConvResponse
	if err := json.NewDecoder(resp.Body).Decode(&convResponse); err != nil {
		return "", err
	}

	if len(convResponse.Records) > 0 && convResponse.Records[0].PMCID != "" {
		return convResponse.Records[0].PMCID, nil
	}
	return "", nil // Kein Fehler, aber auch keine PMCID
}

// getLinkFromOA holt den besten Download-Link aus dem PMC OA Feed.
func (f *Fetcher) getLinkFromOA(pmcID string) (string, error) {
	url := fmt.Sprintf("https://www.ncbi.nlm.nih.gov/pmc/utils/oa/oa.fcgi?id=%s", pmcID)
	f.Logger.Debug("Rufe PMC OA Feed URL auf", zap.String("url", url))

	resp, err := httpClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var oaResponse OAResponse
	if err := xml.Unmarshal(body, &oaResponse); err != nil {
		f.Logger.Warn("XML-Parsing des OA-Feeds fehlgeschlagen, versuche Regex-Fallback", zap.Error(err))
	}

	if oaResponse.Error != "" {
		return "", fmt.Errorf("OA feed returned error: %s", oaResponse.Error)
	}

	var pdfLink, tarLink string
	if len(oaResponse.Records) > 0 {
		for _, link := range oaResponse.Records[0].Links {
			if strings.ToLower(link.Format) == "pdf" && link.Href != "" {
				pdfLink = link.Href
				break
			}
			if tarLink == "" && strings.ToLower(link.Format) == "tgz" && link.Href != "" {
				tarLink = link.Href
			}
		}
	}

	// Regex-Fallbacks
	if pdfLink == "" {
		if matches := pdfRegex.FindStringSubmatch(string(body)); len(matches) > 1 {
			pdfLink = matches[1]
		}
	}
	if pdfLink == "" && tarLink == "" {
		if matches := tarRegex.FindStringSubmatch(string(body)); len(matches) > 1 {
			tarLink = matches[1]
		}
	}

	finalLink := pdfLink
	if finalLink == "" {
		finalLink = tarLink
	}
	f.Logger.Debug("Link-Auswahl im OA-Feed", zap.String("pdf_link_found", pdfLink), zap.String("tar_link_found", tarLink), zap.String("selected_link", finalLink))

	normalizedURL := normalizeURL(finalLink)
	f.Logger.Debug("URL Normalisierung", zap.String("raw_url", finalLink), zap.String("normalized_url", normalizedURL))

	return normalizedURL, nil
}

// getLinkFromUnpaywall is removed from here

// buildEsearchURL baut die URL für eine ESearch-Anfrage.
func (f *Fetcher) buildEsearchURL(term string, retmax, retstart int) string {
	base := fmt.Sprintf("%s/esearch.fcgi?db=pubmed&term=%s&retmode=json&retmax=%d&retstart=%d",
		f.Config.PubMedBaseURL, url.QueryEscape(term), retmax, retstart)
	if f.Config.PubMedAPIKey != "" {
		base += "&api_key=" + f.Config.PubMedAPIKey
	}
	return base
}

// normalizeURL stellt sicher, dass eine URL absolut und mit https ist.
func normalizeURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	if strings.HasPrefix(rawURL, "ftp://") {
		return strings.Replace(rawURL, "ftp://", "https://", 1)
	}
	if strings.HasPrefix(rawURL, "//") {
		return "https:" + rawURL
	}
	if strings.HasPrefix(rawURL, "/") {
		return "https://www.ncbi.nlm.nih.gov" + rawURL
	}
	return rawURL
}

// mapArticleToModel wandelt ein XML-Article-Objekt in unser Paper-Modell um.
func mapArticleToModel(article *PubmedArticle) *models.Paper {
	p := &models.Paper{
		PMID:      article.MedlineCitation.PMID,
		Title:     article.MedlineCitation.Article.Title,
		Abstract:  strings.Join(article.MedlineCitation.Article.Abstract.Text, "\n"),
		PublicURL: fmt.Sprintf("https://pubmed.ncbi.nlm.nih.gov/%s/", article.MedlineCitation.PMID),
	}

	for _, author := range article.MedlineCitation.Article.Authors {
		p.Authors += author.Initials + " " + author.LastName + ", "
	}
	p.Authors = strings.TrimRight(p.Authors, ", ")

	for _, id := range article.MedlineCitation.Article.ELocationID {
		if id.IDType == "doi" && id.ValidYN == "Y" {
			p.DOI = id.Value
			break
		}
	}

	pubDate := article.MedlineCitation.Article.Journal.PubDate
	if pubDate.Year != "" {
		month := "01"
		if pubDate.Month != "" {
			parsedMonth, err := time.Parse("Jan", pubDate.Month)
			if err == nil {
				month = fmt.Sprintf("%02d", parsedMonth.Month())
			} else {
				// Fallback für numerische Monate
				tm, err := time.Parse("1", pubDate.Month)
				if err == nil {
					month = fmt.Sprintf("%02d", tm.Month())
				}
			}
		}
		day := "01"
		if pubDate.Day != "" {
			day = pubDate.Day
		}
		dateStr := fmt.Sprintf("%s-%s-%s", pubDate.Year, month, day)
		t, err := time.Parse("2006-01-02", dateStr)
		if err == nil {
			p.StudyDate = &t
		}
	}

	return p
}
