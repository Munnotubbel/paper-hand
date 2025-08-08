package models

import (
	"time"
)

// PaperLink modelliert eine gerichtete Kante: Quelle zitiert Ziel (A cites B)
type PaperLink struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Normalisierte Identifiers (leerer String, wenn unbekannt)
	SourceDOINorm  string `json:"source_doi_norm" gorm:"index:idx_paper_links_unique_edge,unique;size:512;default:''"`
	SourcePMIDNorm string `json:"source_pmid_norm" gorm:"index:idx_paper_links_unique_edge,unique;size:128;default:''"`
	TargetDOINorm  string `json:"target_doi_norm" gorm:"index:idx_paper_links_unique_edge,unique;size:512;default:''"`
	TargetPMIDNorm string `json:"target_pmid_norm" gorm:"index:idx_paper_links_unique_edge,unique;size:128;default:''"`

	// Roh-IDs (wie geliefert), optional
	SourceDOI  string `json:"source_doi"`
	SourcePMID string `json:"source_pmid"`
	TargetDOI  string `json:"target_doi"`
	TargetPMID string `json:"target_pmid"`

	// Optionale Herkunftsangaben
	SourceTable string `json:"source_table"`
	TargetTable string `json:"target_table"`

	// Evidence: Titel, Jahr, Journal etc.
	Evidence []byte `json:"evidence" gorm:"type:jsonb"`
}

func (PaperLink) TableName() string { return "paper_links" }
