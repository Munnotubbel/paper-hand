package providers

import "paper-hand/models"

// Provider ist das Interface, das jeder Such-Provider (z.B. PubMed, EuropePMC) implementieren muss.
type Provider interface {
	// Search führt eine Suche für einen gegebenen Term durch und gibt eine Liste von standardisierten Paper-Modellen zurück.
	Search(term string) ([]*models.Paper, error)

	// Name gibt den eindeutigen Namen des Providers zurück (z.B. "pubmed").
	Name() string
}
