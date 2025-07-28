package europepmc

import "time"

// SearchResponse ist die Top-Level-Struktur der Europe PMC API-Antwort.
type SearchResponse struct {
	ResultList struct {
		Result []Article `json:"result"`
	} `json:"resultList"`
}

// Article repräsentiert einen einzelnen Artikel in der API-Antwort.
type Article struct {
	ID                   string `json:"id"`
	Source               string `json:"source"`
	PMID                 string `json:"pmid"`
	DOI                  string `json:"doi"`
	Title                string `json:"title"`
	AuthorString         string `json:"authorString"`
	JournalTitle         string `json:"journalTitle"`
	FirstPublicationDate string `json:"firstPublicationDate"`
	AbstractText         string `json:"abstractText"`
	FullTextURLList      struct {
		FullTextURL []FullTextURL `json:"fullTextUrl"`
	} `json:"fullTextUrlList"`
	PubTypeList struct {
		PubType []string `json:"pubType"`
	} `json:"pubTypeList"`
	IsOpenAccess string `json:"isOpenAccess"`
}

// FullTextURL repräsentiert einen einzelnen Volltext-Link.
type FullTextURL struct {
	Availability     string `json:"availability"`
	AvailabilityCode string `json:"availabilityCode"`
	DocumentStyle    string `json:"documentStyle"`
	Site             string `json:"site"`
	URL              string `json:"url"`
}

// Hilfsfunktion zum sicheren Parsen von Daten.
func parseEuroDate(dateStr string) *time.Time {
	layouts := []string{"2006-01-02", "2006-01", "2006"}
	for _, layout := range layouts {
		t, err := time.Parse(layout, dateStr)
		if err == nil {
			return &t
		}
	}
	return nil
}
