package services

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"go.uber.org/zap"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// NormalizeOptions steuern die Heuristiken für die Text-Normalisierung
type NormalizeOptions struct {
	NormalizeUnicode      bool    `json:"normalize_unicode"`
	FixHyphenation        bool    `json:"fix_hyphenation"`
	CollapseWhitespace    bool    `json:"collapse_whitespace"`
	HeaderFooterDetection bool    `json:"header_footer_detection"`
	HeaderFooterThreshold float64 `json:"header_footer_threshold"`
	MinArtifactLineLen    int     `json:"min_artifact_line_len"`
	KeepPageBreaks        bool    `json:"keep_page_breaks"`
	LanguageHint          string  `json:"language_hint"`
}

// Page repräsentiert normalisierten Seitentext
type Page struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

// Stats enthält Kennzahlen zur Normalisierung
type Stats struct {
	NumPages       int `json:"num_pages,omitempty"`
	NumWords       int `json:"num_words"`
	NumChars       int `json:"num_chars"`
	HyphenFixes    int `json:"hyphen_fixes"`
	HeadersRemoved int `json:"headers_removed"`
	FootersRemoved int `json:"footers_removed"`
	DroppedLines   int `json:"dropped_lines"`
}

// NormalizedText bündelt Ergebnis der Normalisierung
type NormalizedText struct {
	FullText string   `json:"full_text"`
	Pages    []Page   `json:"pages,omitempty"`
	Stats    Stats    `json:"stats"`
	Warnings []string `json:"warnings"`
}

// TextNormalizer implementiert die Kernlogik
type TextNormalizer struct {
	logger *zap.Logger
}

func NewTextNormalizer(logger *zap.Logger) *TextNormalizer {
	return &TextNormalizer{logger: logger}
}

// NormalizeExtract normalisiert heterogenen PDF-Extract-Output zu einem Full-Text
func (tn *TextNormalizer) NormalizeExtract(ctx context.Context, extract any, opts NormalizeOptions) (NormalizedText, error) {
	// Defaults
	if opts.HeaderFooterThreshold <= 0 {
		opts.HeaderFooterThreshold = 0.6
	}
	// Rekursiv Strings einsammeln; wenn pages existieren, pro Seite aggregieren
	pageTexts, hasPages := tn.collectPerPageTexts(extract)

	var hyphenFixes, headersRemoved, footersRemoved, droppedLines int
	warnings := []string{}

	var pages []Page
	if hasPages {
		// Header/Footer-Erkennung auf Basis top/bottom-Zeilen je Seite
		headerLines, footerLines := map[string]int{}, map[string]int{}
		if opts.HeaderFooterDetection {
			headerLines, footerLines = tn.detectHeaderFooterLines(pageTexts)
		}

		thresholdCount := int(math.Ceil(opts.HeaderFooterThreshold * float64(len(pageTexts))))

		for i, raw := range pageTexts {
			processed := raw
			if opts.NormalizeUnicode {
				processed = tn.normalizeUnicodeAndLigatures(processed)
			}
			if opts.FixHyphenation {
				var count int
				processed, count = fixHyphenation(processed)
				hyphenFixes += count
			}
			// Header/Footer entfernen (zunächst zeilenweise)
			lines := splitLines(processed)
			var kept []string
			top3 := firstNNonEmpty(lines, 3)
			bot3 := lastNNonEmpty(lines, 3)
			headerSet := make(map[string]bool)
			footerSet := make(map[string]bool)
			for _, l := range top3 {
				if headerLines[strings.TrimSpace(l)] >= thresholdCount || isLikelyPageNumber(l) {
					headerSet[strings.TrimSpace(l)] = true
				}
			}
			for _, l := range bot3 {
				if footerLines[strings.TrimSpace(l)] >= thresholdCount || isLikelyPageNumber(l) {
					footerSet[strings.TrimSpace(l)] = true
				}
			}
			for _, l := range lines {
				trimmed := strings.TrimSpace(l)
				if headerSet[trimmed] {
					// Schütze Zeilen mit Zitierungen
					if ContainsCitation(trimmed) {
						warnings = append(warnings, "header line retained due to detected citation pattern")
					} else {
						headersRemoved++
						continue
					}
				}
				if footerSet[trimmed] {
					if ContainsCitation(trimmed) {
						warnings = append(warnings, "footer line retained due to detected citation pattern")
					} else {
						footersRemoved++
						continue
					}
				}
				kept = append(kept, l)
			}
			processed = strings.Join(kept, "\n")

			if opts.MinArtifactLineLen > 0 {
				var count int
				processed, count = dropArtifactLinesProtectingCitations(processed, opts.MinArtifactLineLen)
				droppedLines += count
			}

			if opts.CollapseWhitespace {
				processed = collapseWhitespace(processed)
			}

			pages = append(pages, Page{Index: i, Text: strings.TrimSpace(processed)})
		}
	}

	// Fallback: kein pages-Feld → alles rekursiv einsammeln und zusammenführen
	var fullText string
	if hasPages {
		if opts.KeepPageBreaks {
			joined := make([]string, 0, len(pages))
			for _, p := range pages {
				joined = append(joined, p.Text)
			}
			fullText = strings.TrimSpace(strings.Join(joined, "\n\n"))
		} else {
			// Zu einem String zusammenführen
			joined := make([]string, 0, len(pages))
			for _, p := range pages {
				joined = append(joined, p.Text)
			}
			fullText = strings.TrimSpace(strings.Join(joined, "\n\n"))
			fullText = strings.ReplaceAll(fullText, "\n\n", "\n\n")
		}
	} else {
		allStrings := collectAllStrings(extract)
		// Sortieren, um deterministische Reihenfolge zu fördern (Objekt-Iteration ist zufällig)
		sort.Strings(allStrings)
		fullText = strings.TrimSpace(strings.Join(allStrings, "\n\n"))
		if opts.NormalizeUnicode {
			fullText = tn.normalizeUnicodeAndLigatures(fullText)
		}
		if opts.FixHyphenation {
			var count int
			fullText, count = fixHyphenation(fullText)
			hyphenFixes += count
		}
		if opts.MinArtifactLineLen > 0 {
			var count int
			fullText, count = dropArtifactLines(fullText, opts.MinArtifactLineLen)
			droppedLines += count
		}
		if opts.CollapseWhitespace {
			fullText = collapseWhitespace(fullText)
		}
	}

	numWords := wordCount(fullText)
	stats := Stats{
		NumPages:       len(pages),
		NumWords:       numWords,
		NumChars:       len([]rune(fullText)),
		HyphenFixes:    hyphenFixes,
		HeadersRemoved: headersRemoved,
		FootersRemoved: footersRemoved,
		DroppedLines:   droppedLines,
	}

	if len(strings.TrimSpace(fullText)) == 0 {
		return NormalizedText{}, errors.New("no text extracted")
	}

	result := NormalizedText{FullText: fullText, Stats: stats, Warnings: warnings}
	if hasPages {
		result.Pages = pages
	}
	return result, nil
}

// collectPerPageTexts versucht, pro Seite Text zu extrahieren, falls ein pages-Feld existiert
func (tn *TextNormalizer) collectPerPageTexts(extract any) ([]string, bool) {
	m, ok := extract.(map[string]any)
	if !ok {
		// Falls kein Objekt, dennoch vollständig einsammeln
		aggregated := collectAllStrings(extract)
		if len(aggregated) == 0 {
			return nil, false
		}
		return []string{strings.Join(aggregated, "\n\n")}, false
	}

	pagesVal, ok := m["pages"]
	if !ok {
		aggregated := collectAllStrings(extract)
		if len(aggregated) == 0 {
			return nil, false
		}
		return []string{strings.Join(aggregated, "\n\n")}, false
	}

	arr, ok := pagesVal.([]any)
	if !ok || len(arr) == 0 {
		aggregated := collectAllStrings(extract)
		if len(aggregated) == 0 {
			return nil, false
		}
		return []string{strings.Join(aggregated, "\n\n")}, false
	}

	pageTexts := make([]string, 0, len(arr))
	for _, page := range arr {
		stringsInPage := collectAllStrings(page)
		// Sortieren, damit deterministischer Join entsteht
		sort.Strings(stringsInPage)
		pageTexts = append(pageTexts, strings.Join(stringsInPage, "\n"))
	}
	return pageTexts, true
}

// detectHeaderFooterLines sammelt Top/Bottom-Zeilen über Seiten und zählt Häufigkeiten
func (tn *TextNormalizer) detectHeaderFooterLines(pageTexts []string) (map[string]int, map[string]int) {
	headerCounts := map[string]int{}
	footerCounts := map[string]int{}
	for _, text := range pageTexts {
		lines := splitLines(text)
		for _, l := range firstNNonEmpty(lines, 3) {
			key := strings.TrimSpace(l)
			if key != "" {
				headerCounts[key]++
			}
		}
		for _, l := range lastNNonEmpty(lines, 3) {
			key := strings.TrimSpace(l)
			if key != "" {
				footerCounts[key]++
			}
		}
	}
	return headerCounts, footerCounts
}

// normalizeUnicodeAndLigatures führt NFC-Normalisierung durch und ersetzt gängige Ligaturen
func (tn *TextNormalizer) normalizeUnicodeAndLigatures(s string) string {
	replacer := strings.NewReplacer(
		"ﬁ", "fi",
		"ﬂ", "fl",
		"ﬀ", "ff",
		"ﬃ", "ffi",
		"ﬄ", "ffl",
		"ﬆ", "st",
		"œ", "oe",
		"æ", "ae",
	)
	s = replacer.Replace(s)
	t := transform.Chain(norm.NFC)
	normalized, _, _ := transform.String(t, s)
	return normalized
}

// fixHyphenation entfernt Trennstriche am Zeilenende zwischen Wort und kleinem Anfangsbuchstaben der Folgelinie
func fixHyphenation(s string) (string, int) {
	// Beispiel: "ab-\nweichung" -> "abweichung"
	re := regexp.MustCompile(`(?m)([\p{L}\p{N}])-(?:\r?\n)([\p{Ll}])`)
	// Zähle Treffer vor Ersetzung
	matches := re.FindAllStringIndex(s, -1)
	count := len(matches)
	if count == 0 {
		return s, 0
	}
	return re.ReplaceAllString(s, "$1$2"), count
}

func collapseWhitespace(s string) string {
	// Mehrfache Spaces zu einem Space; mehr als zwei aufeinanderfolgende Zeilenumbrüche auf zwei begrenzen
    // Use a normal string literal so \u00A0 is interpreted as NBSP before reaching the regex engine
    spaceRE := regexp.MustCompile("[\t\f\v\u00A0]+")
	s = spaceRE.ReplaceAllString(s, " ")
	multiSpace := regexp.MustCompile(` {2,}`)
	s = multiSpace.ReplaceAllString(s, " ")
	multiNewlines := regexp.MustCompile(`\n{3,}`)
	s = multiNewlines.ReplaceAllString(s, "\n\n")
	// Trim per Zeile
	lines := splitLines(s)
	for i := range lines {
		lines[i] = strings.TrimRightFunc(lines[i], unicode.IsSpace)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func dropArtifactLines(s string, minLen int) (string, int) {
	lines := splitLines(s)
	var kept []string
	dropped := 0
	for _, l := range lines {
		visible := countVisibleRunes(l)
		if visible < minLen {
			// Zeilen mit nur Ziffern gelten ebenfalls als Artefakt
			if isLikelyPageNumber(l) || strings.TrimSpace(l) == "" {
				dropped++
				continue
			}
			// kurze Artefakte verwerfen
			dropped++
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n"), dropped
}

// dropArtifactLinesProtectingCitations verwirft kurze Artefaktzeilen, schützt aber erkannte Zitierungen
func dropArtifactLinesProtectingCitations(s string, minLen int) (string, int) {
	lines := splitLines(s)
	var kept []string
	dropped := 0
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			kept = append(kept, l)
			continue
		}
		// Wenn die Zeile eine Zitierung enthält, niemals droppen
		if ContainsCitation(trimmed) {
			kept = append(kept, l)
			continue
		}
		visible := countVisibleRunes(l)
		if visible < minLen {
			if isLikelyPageNumber(l) {
				dropped++
				continue
			}
			dropped++
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n"), dropped
}

func countVisibleRunes(s string) int {
	count := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			count++
		}
	}
	return count
}

func isLikelyPageNumber(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	// numeric-only or like "Page 12"/"12/45"
	if regexp.MustCompile(`^(?:[Pp]age\s*)?\d+(?:\s*/\s*\d+)?$`).MatchString(trimmed) {
		return true
	}
	return false
}

func splitLines(s string) []string {
	// normalisiere Windows-Zeilenumbrüche
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}

func firstNNonEmpty(lines []string, n int) []string {
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, l)
		if len(out) == n {
			break
		}
	}
	return out
}

func lastNNonEmpty(lines []string, n int) []string {
	var out []string
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		out = append(out, lines[i])
		if len(out) == n {
			break
		}
	}
	// reverse to preserve order
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// collectAllStrings sammelt rekursiv alle string-Blätter in beliebigen Strukturen
func collectAllStrings(v any) []string {
	acc := []string{}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			s := strings.TrimSpace(t)
			if s != "" {
				acc = append(acc, s)
			}
		case []any:
			for _, it := range t {
				walk(it)
			}
		case map[string]any:
			// stabile Reihenfolge der keys, um deterministisches Ergebnis zu fördern
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		default:
			// Versuch JSON zu dekodieren, wenn es z. B. aus n8n als map[string]interface{} kommt
			// oder ignorieren, wenn kein bekannter Typ
			// Kein Fehler, um robust gegen heterogene Strukturen zu sein
			_ = t
		}
	}
	walk(v)
	return acc
}

// TryNormalizeJSON is a helper to coerce raw JSON into interface{} with concrete maps/slices
func TryNormalizeJSON(raw []byte) (any, error) {
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func wordCount(s string) int {
	fields := regexp.MustCompile(`\s+`).Split(strings.TrimSpace(s), -1)
	if len(fields) == 1 && fields[0] == "" {
		return 0
	}
	return len(fields)
}
