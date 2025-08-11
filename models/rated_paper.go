package models

import (
    "time"
    "gorm.io/datatypes"
)

// RatedPaper speichert das Analyseergebnis einer KI für ein wissenschaftliches Paper.
type RatedPaper struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Schlüssel, Rating & Asset-Link
	DOI             string  `json:"doi" gorm:"column:doi;uniqueIndex;not null"`
	S3Link          string  `json:"s3_link,omitempty" gorm:"type:text"`
	Rating          float64 `json:"rating"`
	ConfidenceScore float64 `json:"confidence_score,omitempty"`
	Category        string  `json:"category" gorm:"index"`

	// Strukturierte AI-Analyse
	AiSummary        string `json:"ai_summary,omitempty" gorm:"type:text"`
	KeyFindings      string `json:"key_findings,omitempty" gorm:"type:text"` // Kann als JSON-String gespeichert werden
	StudyStrengths   string `json:"study_strengths,omitempty" gorm:"type:text"`
	StudyLimitations string `json:"study_limitations,omitempty" gorm:"type:text"`

	// Content-Workflow
	Outline       string `json:"outline"`
	Citations     string `json:"citations"`
	DeepResearch  string `json:"deep_research"`
	ContentIdea   string `json:"content_idea,omitempty" gorm:"type:text"`
	ContentStatus string `json:"content_status,omitempty" gorm:"index"`
	ContentURL    string `json:"content_url,omitempty"`

	// Status
	Processed bool `json:"processed" gorm:"default:false"`
	AddedRag  bool `json:"added_rag" gorm:" default:false"`
	// LightRAG integration
    LightRAGDocID  string         `json:"lightrag_doc_id" gorm:"index"`
    ReferencesJSON datatypes.JSON `json:"references_json" gorm:"type:jsonb"`
}

// TableName gibt explizit den Tabellennamen an.
func (RatedPaper) TableName() string {
	return "rated_papers"
}
