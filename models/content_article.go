package models

import "time"

// ContentArticle repräsentiert einen veröffentlichten oder geplanten Content-Artikel basierend auf einem bewerteten Paper.
type ContentArticle struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Referenz-Informationen
	Substance string  `json:"substance" gorm:"index"`
	PMID      string  `json:"pmid" gorm:"column:pmid;index"`
	DOI       string  `json:"doi" gorm:"column:doi;index"`
	StudyLink string  `json:"study_link,omitempty"`
	Rating    float64 `json:"rating"`

	// Content-Daten
	Title      string `json:"title" gorm:"not null"`
	Subtitle   string `json:"subtitle,omitempty"`
	Text       string `json:"text" gorm:"type:text"`
	PictureURL string `json:"picture_url,omitempty"`

	// Meta-Informationen
	StudyType        string     `json:"study_type,omitempty"`
	StudyReleaseDate *time.Time `json:"study_release_date,omitempty"`

	// Content Management
	ContentStatus string     `json:"content_status" gorm:"index;default:'draft'"` // draft, review, published, archived
	PublishedAt   *time.Time `json:"published_at,omitempty"`
	AuthorName    string     `json:"author_name,omitempty"`
	BlogPosted    bool       `json:"blog_posted" gorm:"default:false"`

	// SEO & Web
	MetaDescription string `json:"meta_description,omitempty"`
	Slug            string `json:"slug,omitempty" gorm:"uniqueIndex"`

	// Kategorisierung
	Category string `json:"category,omitempty" gorm:"index"`
	Tags     string `json:"tags,omitempty"` // JSON string mit Tags

	// Analytics
	ViewCount int `json:"view_count" gorm:"default:0"`
}

// TableName gibt explizit den Tabellennamen an.
func (ContentArticle) TableName() string {
	return "content_articles"
}
