// Package pubmed enthält die Logik für die Interaktion mit der PubMed/PMC API.
package pubmed

import (
	"encoding/xml"
)

// ESearchResponse repräsentiert die JSON-Antwort von ESearch für die ID-Suche.
type ESearchResponse struct {
	ESearchResult struct {
		IdList []string `json:"idlist"`
	} `json:"esearchresult"`
}

// IDConvResponse repräsentiert die JSON-Antwort des PMC ID Converters.
type IDConvResponse struct {
	Records []struct {
		PMCID string `json:"pmcid"`
	} `json:"records"`
}

// OAResponse repräsentiert die XML-Antwort des PMC Open Access Interface.
type OAResponse struct {
	XMLName xml.Name   `xml:"OA"`
	Error   string     `xml:"error"`
	Records []OARecord `xml:"records>record"`
}

// OARecord repräsentiert einen einzelnen Record im OA-Feed.
type OARecord struct {
	Links []OALink `xml:"link"`
}

// OALink repräsentiert einen Download-Link im OA-Record.
type OALink struct {
	Format string `xml:"format,attr"`
	Href   string `xml:"href,attr"`
}

// PubmedArticleSet repräsentiert das gesamte XML-Dokument von efetch.
type PubmedArticleSet struct {
	XMLName       xml.Name        `xml:"PubmedArticleSet"`
	PubmedArticle []PubmedArticle `xml:"PubmedArticle"`
}

// PubmedArticle repräsentiert einen einzelnen Artikel in der XML-Antwort.
type PubmedArticle struct {
	MedlineCitation struct {
		PMID    string `xml:"PMID"`
		Article struct {
			Title    string `xml:"ArticleTitle"`
			Abstract struct {
				Text []string `xml:"AbstractText"`
			} `xml:"Abstract"`
			Authors []struct {
				LastName string `xml:"LastName"`
				Initials string `xml:"Initials"`
			} `xml:"AuthorList>Author"`
			Journal struct {
				PubDate struct {
					Year  string `xml:"Year"`
					Month string `xml:"Month"`
					Day   string `xml:"Day"`
				} `xml:"JournalIssue>PubDate"`
			} `xml:"Journal"`
			ELocationID []struct {
				IDType  string `xml:"EIdType,attr"`
				ValidYN string `xml:"ValidYN,attr"`
				Value   string `xml:",chardata"`
			} `xml:"ELocationID"`
		} `xml:"Article"`
	} `xml:"MedlineCitation"`
}
