package services

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"go.uber.org/zap"
)

// CitationExtractor extrahiert Zitierungen und Referenzen aus wissenschaftlichen Texten
type CitationExtractor struct {
	Logger *zap.Logger
}

// CitationResult enthält alle extrahierten Zitierungen und Referenzen
type CitationResult struct {
	InTextCitations  []string            `json:"in_text_citations"`
	FullReferences   []string            `json:"full_references"`
	CitationCount    int                 `json:"citation_count"`
	ReferenceCount   int                 `json:"reference_count"`
	CitationPatterns map[string][]string `json:"citation_patterns"`
	CitationMappings []CitationMapping   `json:"citation_mappings"`
}

// CitationMapping verknüpft Original-Aussagen mit ihren Zitierungen
type CitationMapping struct {
	OriginalSentence string   `json:"original_sentence"`
	Citations        []string `json:"citations"`
	Keywords         []string `json:"keywords"`
	Concepts         []string `json:"concepts"`
	SentenceID       string   `json:"sentence_id"`
}

// NewCitationExtractor erstellt einen neuen Citation-Extraktor
func NewCitationExtractor(logger *zap.Logger) *CitationExtractor {
	return &CitationExtractor{
		Logger: logger,
	}
}

// ExtractCitations extrahiert alle Zitierungen und Referenzen aus einem wissenschaftlichen Text
func (ce *CitationExtractor) ExtractCitations(ctx context.Context, text string) (*CitationResult, error) {
	ce.Logger.Info("Starting citation extraction",
		zap.Int("text_length", len(text)))

	result := &CitationResult{
		InTextCitations:  []string{},
		FullReferences:   []string{},
		CitationPatterns: make(map[string][]string),
	}

	// 1. Extrahiere In-Text-Zitierungen mit verschiedenen Patterns
	ce.extractInTextCitations(text, result)

	// 2. Extrahiere vollständige Referenzen aus dem Literaturverzeichnis
	ce.extractFullReferences(text, result)

	// 3. Erstelle Citation-Mappings für intelligente Zitation-Übertragung
	ce.createCitationMappings(text, result)

	// 4. Counts setzen
	result.CitationCount = len(result.InTextCitations)
	result.ReferenceCount = len(result.FullReferences)

	ce.Logger.Info("Citation extraction completed",
		zap.Int("in_text_citations", result.CitationCount),
		zap.Int("full_references", result.ReferenceCount))

	return result, nil
}

// extractInTextCitations findet alle In-Text-Zitierungen
func (ce *CitationExtractor) extractInTextCitations(text string, result *CitationResult) {
	// Definition verschiedener Citation-Patterns - ERWEITERT für maximale Abdeckung
	patterns := map[string]*regexp.Regexp{
		// === AUTHOR-YEAR STYLES ===
		"author_year":        regexp.MustCompile(`\([A-Z][a-zA-Z\s&,]+\s+et\s+al\.?,?\s*\d{4}[a-z]?\)`),
		"author_year_simple": regexp.MustCompile(`\([A-Z][a-zA-Z\s&,]+,?\s*\d{4}[a-z]?\)`),
		"author_year_pages":  regexp.MustCompile(`\([A-Z][a-zA-Z\s&,]+\s+et\s+al\.?,?\s*\d{4}[a-z]?,?\s*pp?\.\s*\d+[-–]?\d*\)`), // (Smith et al., 2020, p. 15)

		// === NUMERIC STYLES (Verbessert für echte Zitierungen) ===
		"numeric_brackets":          regexp.MustCompile(`\[\d{1,3}(?:[-–,\s]*\d{1,3}){0,4}\]`),         // Max 5 refs, 1-3 Stellen
		"numeric_parens_after_word": regexp.MustCompile(`\w\s*\(\d{1,3}(?:[-–,\s]*\d{1,3}){0,4}\)`),    // Nur nach Wörtern
		"vancouver_after_sentence":  regexp.MustCompile(`[.!?]\s*\d{1,3}(?:,\s*\d{1,3}){0,4}\s+[A-Z]`), // Nach Sätzen vor Großbuchstaben

		// === SUPERSCRIPT STYLES ===
		"superscript_unicode": regexp.MustCompile(`[¹²³⁴⁵⁶⁷⁸⁹⁰]+`),        // ¹²³
		"superscript_text":    regexp.MustCompile(`\^[\d,\s-]+\^`),        // ^1,2,3^
		"superscript_markup":  regexp.MustCompile(`<sup>[\d,\s-]+</sup>`), // <sup>1,2,3</sup>

		// === MIXED & COMPLEX ===
		"multiple_authors": regexp.MustCompile(`\([A-Z][a-zA-Z\s&,]+\s+et\s+al\.?\s*[;,]\s*\d{4}[a-z]?(?:\s*[;,]\s*[A-Z][a-zA-Z\s&,]+\s+et\s+al\.?\s*[;,]\s*\d{4}[a-z]?)*\)`),
		"doi_citations":    regexp.MustCompile(`doi:\s*10\.\d+[^\s]*`), // DOI-only citations
		"footnote_markers": regexp.MustCompile(`[¹²³⁴⁵⁶⁷⁸⁹⁰]+|\d+`),    // Footnote markers
	}

	citationSet := make(map[string]bool)

	for patternName, pattern := range patterns {
		matches := pattern.FindAllString(text, -1)
		result.CitationPatterns[patternName] = matches

		for _, match := range matches {
			cleanMatch := strings.TrimSpace(match)
			if !citationSet[cleanMatch] {
				citationSet[cleanMatch] = true
				result.InTextCitations = append(result.InTextCitations, cleanMatch)
			}
		}
	}

	// Sortiere Zitierungen alphabetisch
	sort.Strings(result.InTextCitations)
}

// extractFullReferences extrahiert vollständige Referenzen aus dem Text
func (ce *CitationExtractor) extractFullReferences(text string, result *CitationResult) {
	// Verschiedene Abschnittsnamen für Literaturverzeichnis
	refSections := []string{
		"References",
		"Bibliography",
		"Literature",
		"Citations",
		"Works Cited",
		"Literaturverzeichnis",
		"Literatur",
		"Quellen",
		"Sources",
	}

	// Finde Literaturverzeichnis-Abschnitt
	var refSectionStart int = -1

	for _, section := range refSections {
		// Pattern für Abschnitts-Überschriften
		patterns := []*regexp.Regexp{
			regexp.MustCompile(`(?i)^\s*` + section + `\s*$`),
			regexp.MustCompile(`(?i)^##?\s*` + section + `\s*$`),
			regexp.MustCompile(`(?i)^[0-9]+\.?\s*` + section + `\s*$`),
		}

		for _, pattern := range patterns {
			lines := strings.Split(text, "\n")
			for i, line := range lines {
				if pattern.MatchString(strings.TrimSpace(line)) {
					refSectionStart = i
					ce.Logger.Debug("Found references section",
						zap.String("section", section),
						zap.Int("start_line", i))
					break
				}
			}
			if refSectionStart != -1 {
				break
			}
		}
		if refSectionStart != -1 {
			break
		}
	}

	if refSectionStart == -1 {
		ce.Logger.Warn("No references section found, trying to extract from entire text")
		refSectionStart = 0
	}

	// Extrahiere Referenzen aus dem gefundenen Abschnitt
	lines := strings.Split(text, "\n")
	if refSectionStart < len(lines) {
		refLines := lines[refSectionStart:]

		for _, line := range refLines {
			line = strings.TrimSpace(line)

			// Überspringen von leeren Zeilen und Überschriften
			if line == "" || isHeaderLine(line) {
				continue
			}

			// Pattern für typische Referenz-Formate
			if isValidReference(line) {
				result.FullReferences = append(result.FullReferences, line)
			}
		}
	}

	ce.Logger.Debug("Extracted full references",
		zap.Int("count", len(result.FullReferences)),
		zap.Int("section_start", refSectionStart))
}

// isHeaderLine prüft ob eine Zeile eine Überschrift ist
func isHeaderLine(line string) bool {
	headerPatterns := []*regexp.Regexp{
		regexp.MustCompile(`^#{1,6}\s+.*$`),         // Markdown headers
		regexp.MustCompile(`^[0-9]+\.?\s*[A-Z].*$`), // Numbered headers
		regexp.MustCompile(`^[A-Z\s]+$`),            // ALL CAPS headers
		regexp.MustCompile(`^(References|Bibliography|Literature|Citations|Works Cited|Literaturverzeichnis|Literatur|Quellen|Sources)\s*$`), // Section headers
	}

	for _, pattern := range headerPatterns {
		if pattern.MatchString(line) {
			return true
		}
	}
	return false
}

// isValidReference prüft ob eine Zeile eine gültige Referenz ist - ERWEITERT
func isValidReference(line string) bool {
	// Minimum-Länge für eine Referenz
	if len(line) < 15 { // Reduziert für kürzere DOI-only refs
		return false
	}

	// Pattern für typische Referenz-Eigenschaften - ERWEITERT
	referencePatterns := []*regexp.Regexp{
		// === STANDARD PATTERNS ===
		regexp.MustCompile(`[A-Z][a-zA-Z\s,&]+\s*\(\d{4}[a-z]?\)`), // Autor (Jahr)
		regexp.MustCompile(`[A-Z][a-zA-Z\s,&]+\.\s*\d{4}[a-z]?`),   // Autor. Jahr

		// === JOURNAL PATTERNS ===
		regexp.MustCompile(`\.\s+[A-Z][a-zA-Z\s&]+,\s*\d+`), // Journal, Volume
		regexp.MustCompile(`\d+\(\d+\):\s*\d+[-–]\d+`),      // Vol(Issue): pages
		regexp.MustCompile(`vol\.\s*\d+`),                   // vol. X

		// === DIGITAL IDENTIFIERS ===
		regexp.MustCompile(`doi:\s*10\.\d+[^\s]*`), // DOI
		regexp.MustCompile(`pmid:\s*\d+`),          // PMID
		regexp.MustCompile(`isbn:\s*[\d-]+`),       // ISBN
		regexp.MustCompile(`arxiv:\s*[\d.v]+`),     // ArXiv

		// === URLS & LINKS ===
		regexp.MustCompile(`https?://[^\s]+`), // URLs
		regexp.MustCompile(`www\.[^\s]+`),     // www links

		// === PUBLISHING INFO ===
		regexp.MustCompile(`pp?\.\s*\d+[-–]\d+`),             // pages
		regexp.MustCompile(`[Pp]ublished|[Pp]ress|[Pp]rint`), // Publishers
		regexp.MustCompile(`ed\.|editor|edited`),             // Editors

		// === NUMERIC REFERENCES ===
		regexp.MustCompile(`^\d+\.\s+[A-Z]`),   // 1. Author
		regexp.MustCompile(`^\[\d+\]\s+[A-Z]`), // [1] Author
	}

	// Muss mindestens ein Pattern matchen
	for _, pattern := range referencePatterns {
		if pattern.MatchString(line) {
			return true
		}
	}

	return false
}

// createCitationMappings erstellt intelligente Mappings zwischen Sätzen und Zitierungen
func (ce *CitationExtractor) createCitationMappings(text string, result *CitationResult) {
	ce.Logger.Debug("Creating citation mappings")

	// Entferne References-Sektion für Mapping (nur Haupttext)
	mainText := ce.getMainTextOnly(text)

	// Teile Text in Sätze auf
	sentences := ce.splitIntoSentences(mainText)

	// Erstelle für jeden Satz mit Zitierungen ein Mapping
	for i, sentence := range sentences {
		citations := ce.findCitationsInSentence(sentence, result.InTextCitations)

		if len(citations) > 0 {
			mapping := CitationMapping{
				OriginalSentence: strings.TrimSpace(sentence),
				Citations:        citations,
				Keywords:         ce.extractKeywords(sentence),
				Concepts:         ce.extractConcepts(sentence),
				SentenceID:       ce.generateSentenceID(sentence, i),
			}

			result.CitationMappings = append(result.CitationMappings, mapping)
		}
	}

	ce.Logger.Debug("Citation mappings created",
		zap.Int("mappings_count", len(result.CitationMappings)))
}

// getMainTextOnly entfernt das Literaturverzeichnis für das Mapping
func (ce *CitationExtractor) getMainTextOnly(text string) string {
	refSections := []string{"References", "Bibliography", "Literature", "Literatur", "Quellen"}

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		for _, section := range refSections {
			if regexp.MustCompile(`(?i)^\s*#+?\s*` + section + `\s*$`).MatchString(strings.TrimSpace(line)) {
				// Gib nur den Text vor dem References-Abschnitt zurück
				return strings.Join(lines[:i], "\n")
			}
		}
	}
	return text // Falls keine References-Sektion gefunden
}

// splitIntoSentences teilt Text intelligent in Sätze auf
func (ce *CitationExtractor) splitIntoSentences(text string) []string {
	// Einfache aber effektive Satz-Trennung
	// Achtung: Wissenschaftliche Texte haben viele Abkürzungen!

	sentences := []string{}

	// Ersetze bekannte Abkürzungen temporär
	protected := text
	abbreviations := []string{"et al.", "i.e.", "e.g.", "cf.", "vs.", "etc.", "Dr.", "Prof.", "Fig.", "Tab."}
	for i, abbr := range abbreviations {
		placeholder := fmt.Sprintf("__ABBR_%d__", i)
		protected = strings.ReplaceAll(protected, abbr, placeholder)
	}

	// Teile bei Punkt + Leerzeichen + Großbuchstabe
	sentenceRegex := regexp.MustCompile(`([.!?])\s+([A-Z])`)
	parts := sentenceRegex.Split(protected, -1)

	if len(parts) > 1 {
		// Füge die Split-Zeichen wieder hinzu
		matches := sentenceRegex.FindAllStringSubmatch(protected, -1)
		for i, part := range parts[:len(parts)-1] {
			if i < len(matches) {
				part += matches[i][1] // Füge Punkt/!/?  wieder hinzu
			}
			sentences = append(sentences, part)
		}
		sentences = append(sentences, parts[len(parts)-1]) // Letzter Teil
	} else {
		sentences = []string{protected}
	}

	// Stelle Abkürzungen wieder her
	for i := range sentences {
		for j, abbr := range abbreviations {
			placeholder := fmt.Sprintf("__ABBR_%d__", j)
			sentences[i] = strings.ReplaceAll(sentences[i], placeholder, abbr)
		}
		sentences[i] = strings.TrimSpace(sentences[i])
	}

	// Entferne leere Sätze
	var result []string
	for _, sentence := range sentences {
		if len(strings.TrimSpace(sentence)) > 10 { // Mindestlänge
			result = append(result, sentence)
		}
	}

	return result
}

// findCitationsInSentence findet alle Zitierungen in einem spezifischen Satz
func (ce *CitationExtractor) findCitationsInSentence(sentence string, allCitations []string) []string {
	found := []string{}

	for _, citation := range allCitations {
		if strings.Contains(sentence, citation) {
			found = append(found, citation)
		}
	}

	return found
}

// extractKeywords extrahiert wichtige Keywords aus einem Satz
func (ce *CitationExtractor) extractKeywords(sentence string) []string {
	keywords := []string{}

	// Einfache Keyword-Extraktion: Wörter > 4 Zeichen, keine Stopwords
	stopwords := map[string]bool{
		"that": true, "this": true, "with": true, "from": true, "they": true,
		"were": true, "been": true, "have": true, "their": true, "said": true,
		"each": true, "which": true, "them": true, "than": true, "many": true,
		"some": true, "these": true, "would": true, "there": true, "what": true,
		"und": true, "eine": true, "einer": true, "eines": true, "dass": true,
		"sich": true, "sind": true, "wird": true, "wurde": true, "wurden": true,
		"werden": true, "kann": true, "können": true, "durch": true, "über": true,
	}

	// Extrahiere Wörter
	wordRegex := regexp.MustCompile(`\b[A-Za-z]+\b`)
	words := wordRegex.FindAllString(sentence, -1)

	for _, word := range words {
		cleaned := strings.ToLower(strings.TrimSpace(word))
		if len(cleaned) > 4 && !stopwords[cleaned] {
			// Prüfe ob es ein wissenschaftlicher Begriff sein könnte
			if ce.isScientificTerm(cleaned) {
				keywords = append(keywords, cleaned)
			}
		}
	}

	// Entferne Duplikate
	seen := make(map[string]bool)
	var unique []string
	for _, keyword := range keywords {
		if !seen[keyword] {
			seen[keyword] = true
			unique = append(unique, keyword)
		}
	}

	return unique
}

// extractConcepts extrahiert wissenschaftliche Konzepte und Substanzen
func (ce *CitationExtractor) extractConcepts(sentence string) []string {
	concepts := []string{}

	// Pattern für wissenschaftliche Konzepte
	conceptPatterns := []*regexp.Regexp{
		regexp.MustCompile(`\b[A-Z][a-z]+(?:in|mine|cin|ide|ase|ose)\b`), // Curcumin, Dopamine, etc.
		regexp.MustCompile(`\b[A-Z]{2,}\b`),                              // Abkürzungen wie TNF, IL-6
		regexp.MustCompile(`\b\d*[A-Z]\d*\b`),                            // Vitamin C, B12, etc.
		regexp.MustCompile(`\banti-\w+\b`),                               // anti-inflammatory, anti-oxidant
		regexp.MustCompile(`\b\w+therapy\b`),                             // chemotherapy, radiotherapy
		regexp.MustCompile(`\b\w+disease\b`),                             // heart disease, cancer
	}

	for _, pattern := range conceptPatterns {
		matches := pattern.FindAllString(sentence, -1)
		for _, match := range matches {
			concepts = append(concepts, strings.ToLower(match))
		}
	}

	// Spezifische medizinische/wissenschaftliche Begriffe
	medicalTerms := regexp.MustCompile(`\b(?:cancer|tumor|inflammation|oxidative|therapeutic|clinical|efficacy|bioavailability|metabolism|pharmacokinetic|antioxidant|neuroprotective|cardioprotective|hepatoprotective|chemopreventive)\b`)
	matches := medicalTerms.FindAllString(strings.ToLower(sentence), -1)
	concepts = append(concepts, matches...)

	// Entferne Duplikate
	seen := make(map[string]bool)
	var unique []string
	for _, concept := range concepts {
		if !seen[concept] {
			seen[concept] = true
			unique = append(unique, concept)
		}
	}

	return unique
}

// isScientificTerm prüft ob ein Wort ein wissenschaftlicher Begriff sein könnte
func (ce *CitationExtractor) isScientificTerm(word string) bool {
	// Heuristics für wissenschaftliche Begriffe
	scientificSuffixes := []string{"tion", "ism", "ment", "ness", "ity", "ogy", "ics", "ine", "ase", "ose"}
	scientificPrefixes := []string{"anti", "pro", "pre", "post", "inter", "intra", "extra", "trans"}

	for _, suffix := range scientificSuffixes {
		if strings.HasSuffix(word, suffix) {
			return true
		}
	}

	for _, prefix := range scientificPrefixes {
		if strings.HasPrefix(word, prefix) {
			return true
		}
	}

	// Enthält es Großbuchstaben in der Mitte? (CamelCase = oft wissenschaftlich)
	for i, r := range word[1:] { // Skip first char
		if unicode.IsUpper(r) && i > 0 {
			return true
		}
	}

	return false
}

// generateSentenceID erstellt eine eindeutige ID für einen Satz
func (ce *CitationExtractor) generateSentenceID(sentence string, index int) string {
	// MD5 Hash der ersten 50 Zeichen + Index
	prefix := sentence
	if len(prefix) > 50 {
		prefix = prefix[:50]
	}

	hash := md5.Sum([]byte(prefix))
	return fmt.Sprintf("sent_%d_%x", index, hash[:4])
}

// FormatForN8N formatiert das Ergebnis für n8n Workflow
func (ce *CitationExtractor) FormatForN8N(result *CitationResult) string {
	output := "## EXTRAHIERTE IN-TEXT-ZITIERUNGEN:\n"
	for _, citation := range result.InTextCitations {
		output += citation + "\n"
	}

	output += "\n## VOLLSTÄNDIGE REFERENZEN:\n\n"
	for _, reference := range result.FullReferences {
		output += reference + "\n\n"
	}

	output += "## STATISTIKEN:\n"
	output += fmt.Sprintf("- In-Text-Zitierungen: %d\n", result.CitationCount)
	output += fmt.Sprintf("- Vollständige Referenzen: %d\n", result.ReferenceCount)
	output += fmt.Sprintf("- Citation-Mappings: %d\n", len(result.CitationMappings))

	// Füge Citation-Mappings hinzu für Debugging
	if len(result.CitationMappings) > 0 {
		output += "\n## CITATION-MAPPINGS (für AI-Agent-Integration):\n\n"
		for i, mapping := range result.CitationMappings {
			output += fmt.Sprintf("### Mapping %d (ID: %s):\n", i+1, mapping.SentenceID)
			output += fmt.Sprintf("**Original:** %s\n", mapping.OriginalSentence)
			output += fmt.Sprintf("**Zitierungen:** %s\n", strings.Join(mapping.Citations, ", "))
			output += fmt.Sprintf("**Keywords:** %s\n", strings.Join(mapping.Keywords, ", "))
			output += fmt.Sprintf("**Konzepte:** %s\n\n", strings.Join(mapping.Concepts, ", "))
		}
	}

	return output
}

// InjectCitations fügt Zitierungen in einen vereinfachten Text basierend auf Mappings ein
func (ce *CitationExtractor) InjectCitations(ctx context.Context, simplifiedText string, originalMappings []CitationMapping) (string, error) {
	ce.Logger.Info("Starting citation injection",
		zap.Int("text_length", len(simplifiedText)),
		zap.Int("available_mappings", len(originalMappings)))

	if len(originalMappings) == 0 {
		ce.Logger.Warn("No citation mappings available for injection")
		return simplifiedText, nil
	}

	// Teile vereinfachten Text in Sätze auf
	sentences := ce.splitIntoSentences(simplifiedText)
	var enhancedSentences []string

	for _, sentence := range sentences {
		// Finde das beste Mapping für diesen Satz
		bestMapping := ce.findBestMapping(sentence, originalMappings)

		if bestMapping != nil {
			// Limitiere Citations pro Satz (max 3 für Lesbarkeit)
			limitedCitations := bestMapping.Citations
			if len(limitedCitations) > 3 {
				limitedCitations = limitedCitations[:3]
			}

			// Füge Zitierungen hinzu
			enhancedSentence := ce.addCitationsToSentence(sentence, limitedCitations)
			enhancedSentences = append(enhancedSentences, enhancedSentence)

			ce.Logger.Debug("Citation injected",
				zap.String("sentence_preview", sentence[:min(50, len(sentence))]),
				zap.Strings("citations", bestMapping.Citations))
		} else {
			// Keine passende Zitierung gefunden
			enhancedSentences = append(enhancedSentences, sentence)
		}
	}

	result := strings.Join(enhancedSentences, " ")

	ce.Logger.Info("Citation injection completed",
		zap.Int("sentences_processed", len(sentences)),
		zap.Int("citations_injected", ce.CountInjectedCitations(result)))

	return result, nil
}

// findBestMapping findet das beste Mapping für einen Satz basierend auf Ähnlichkeit
func (ce *CitationExtractor) findBestMapping(sentence string, mappings []CitationMapping) *CitationMapping {
	if len(mappings) == 0 {
		return nil
	}

	// Extrahiere Keywords und Konzepte aus dem Satz
	sentenceKeywords := ce.extractKeywords(sentence)
	sentenceConcepts := ce.extractConcepts(sentence)

	var bestMapping *CitationMapping
	var bestScore float64

	for i := range mappings {
		mapping := &mappings[i]

		// Berechne Ähnlichkeits-Score
		score := ce.calculateSimilarityScore(sentenceKeywords, sentenceConcepts, mapping)

		if score > bestScore && score > 0.6 { // Erhöhter Threshold für bessere Präzision
			bestScore = score
			bestMapping = mapping
		}
	}

	return bestMapping
}

// calculateSimilarityScore berechnet wie ähnlich zwei Sätze sind
func (ce *CitationExtractor) calculateSimilarityScore(keywords1, concepts1 []string, mapping *CitationMapping) float64 {
	if len(mapping.Keywords) == 0 && len(mapping.Concepts) == 0 {
		return 0.0
	}

	// Keyword-Überschneidung
	keywordMatches := ce.countOverlap(keywords1, mapping.Keywords)
	keywordScore := float64(keywordMatches) / float64(max(len(keywords1), len(mapping.Keywords)))

	// Konzept-Überschneidung
	conceptMatches := ce.countOverlap(concepts1, mapping.Concepts)
	conceptScore := float64(conceptMatches) / float64(max(len(concepts1), len(mapping.Concepts)))

	// Gewichtete Kombination (Konzepte sind wichtiger als allgemeine Keywords)
	return 0.3*keywordScore + 0.7*conceptScore
}

// countOverlap zählt überschneidende Elemente zwischen zwei Slices
func (ce *CitationExtractor) countOverlap(slice1, slice2 []string) int {
	set := make(map[string]bool)
	for _, item := range slice1 {
		set[item] = true
	}

	count := 0
	for _, item := range slice2 {
		if set[item] {
			count++
		}
	}
	return count
}

// addCitationsToSentence fügt Zitierungen an das Ende eines Satzes hinzu
func (ce *CitationExtractor) addCitationsToSentence(sentence string, citations []string) string {
	if len(citations) == 0 {
		return sentence
	}

	// Entferne Punkt am Ende, falls vorhanden
	cleanSentence := strings.TrimSpace(sentence)
	cleanSentence = strings.TrimSuffix(cleanSentence, ".")

	// Füge Zitierungen hinzu
	citationText := " " + strings.Join(citations, ", ")

	return cleanSentence + citationText + "."
}

// CountInjectedCitations zählt die Anzahl der injizierten Zitierungen (exported for testing)
func (ce *CitationExtractor) CountInjectedCitations(text string) int {
	// Zähle alle Citation-Patterns im Text
	patterns := []string{
		`\([A-Z][a-zA-Z\s&,]+\s+et\s+al\.?,?\s*\d{4}[a-z]?\)`,
		`\([A-Z][a-zA-Z\s&,]+,?\s*\d{4}[a-z]?\)`,
		`\[\d+(?:[-–,\s]*\d+)*\]`,
		`\(\d+(?:[-–,\s]*\d+)*\)`,
		`doi:\s*10\.\d+[^\s]*`,
	}

	count := 0
	for _, pattern := range patterns {
		regex := regexp.MustCompile(pattern)
		matches := regex.FindAllString(text, -1)
		count += len(matches)
	}

	return count
}

// Helper functions
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// RemoveReferencesSection entfernt nur das Literaturverzeichnis, behält In-Text-Zitierungen
func (ce *CitationExtractor) RemoveReferencesSection(ctx context.Context, text string) (string, error) {
	ce.Logger.Info("Removing references section from text",
		zap.Int("original_length", len(text)))

	// Finde Literaturverzeichnis-Abschnitt (gleiche Logik wie in extractFullReferences)
	refSections := []string{
		"References",
		"Bibliography",
		"Literature",
		"Citations",
		"Works Cited",
		"Literaturverzeichnis",
		"Literatur",
		"Quellen",
		"Sources",
	}

	lines := strings.Split(text, "\n")
	refSectionStart := -1

	// Suche nach References-Sektion
	for _, section := range refSections {
		patterns := []*regexp.Regexp{
			regexp.MustCompile(`(?i)^\s*` + section + `\s*$`),
			regexp.MustCompile(`(?i)^##?\s*` + section + `\s*$`),
			regexp.MustCompile(`(?i)^[0-9]+\.?\s*` + section + `\s*$`),
		}

		for _, pattern := range patterns {
			for i, line := range lines {
				if pattern.MatchString(strings.TrimSpace(line)) {
					refSectionStart = i
					ce.Logger.Debug("Found references section to remove",
						zap.String("section", section),
						zap.Int("start_line", i))
					break
				}
			}
			if refSectionStart != -1 {
				break
			}
		}
		if refSectionStart != -1 {
			break
		}
	}

	// Wenn keine References-Sektion gefunden, gib Original zurück
	if refSectionStart == -1 {
		ce.Logger.Info("No references section found, returning original text")
		return text, nil
	}

	// Schneide ab der References-Sektion ab
	cleanedLines := lines[:refSectionStart]
	cleanedText := strings.Join(cleanedLines, "\n")

	// Entferne trailing whitespace
	cleanedText = strings.TrimSpace(cleanedText)

	ce.Logger.Info("References section removed successfully",
		zap.Int("original_lines", len(lines)),
		zap.Int("cleaned_lines", len(cleanedLines)),
		zap.Int("removed_lines", len(lines)-len(cleanedLines)),
		zap.Int("size_reduction_percent", int(float64(len(text)-len(cleanedText))/float64(len(text))*100)))

	return cleanedText, nil
}

// ToJSON konvertiert das Ergebnis zu JSON für API-Response
func (result *CitationResult) ToJSON() ([]byte, error) {
	return json.MarshalIndent(result, "", "  ")
}
