package models

import (
	"time"
)

// Paper repräsentiert die Metadaten eines wissenschaftlichen Artikels.
type Paper struct {
	ID              uint       `json:"id" gorm:"primaryKey"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Substance       string     `json:"substance" gorm:"index"`
	PMID            string     `json:"pmid" gorm:"column:pmid;uniqueIndex;not null;default:''"`
	DOI             string     `json:"doi,omitempty" gorm:"column:doi;uniqueIndex"`
	Title           string     `json:"title"`
	Abstract        string     `json:"abstract,omitempty" gorm:"type:text"`
	StudyDate       *time.Time `json:"study_date,omitempty"`
	Authors         string     `json:"authors,omitempty"`
	PublicURL       string     `json:"public_url,omitempty"`
	DownloadLink    string     `json:"download_link,omitempty"`
	DownloadDate    *time.Time `json:"download_date,omitempty"`
	TransferN8N     bool       `json:"transfer_n8n" gorm:"column:transfer_n8n;default:false"`
	CloudStored     bool       `json:"cloud_stored"`
	StudyType       string     `json:"study_type,omitempty"`
	PublicationType string     `json:"publication_type,omitempty" gorm:"index"`
	StudyDesign     string     `json:"study_design,omitempty" gorm:"index"`
	NoPDFFound      bool       `json:"no_pdf_found"`
	S3Link          string     `json:"s3_link,omitempty"`
}

func (Paper) TableName() string {
	return "papers"
}
