package models

// Substance repräsentiert einen Wirkstoff, nach dem gesucht wird.
type Substance struct {
	ID   uint   `json:"id" gorm:"primaryKey"`
	Name string `json:"name" gorm:"uniqueIndex;not null"` // z.B. "curcumin"
}

// TableName gibt den expliziten Tabellennamen für GORM an.
func (Substance) TableName() string {
	return "substances"
}
