package models

// SearchFilter repräsentiert eine wiederverwendbare Suchstrategie/Filter.
type SearchFilter struct {
	ID          uint   `json:"id" gorm:"primaryKey"`
	Name        string `json:"name" gorm:"uniqueIndex;not null"`       // z.B. "Meta-Analysis (Human)"
	FilterQuery string `json:"filter_query" gorm:"type:text;not null"` // Der PubMed-spezifische Teil des Suchbegriffs
}

// TableName gibt den expliziten Tabellennamen für GORM an.
func (SearchFilter) TableName() string {
	return "search_filters"
}
