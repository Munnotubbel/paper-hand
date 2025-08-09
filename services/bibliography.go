package services

import (
    "fmt"
    "regexp"
    "sort"
    "strings"
)

// SourceItem represents a numbered source used in an answer
type SourceItem struct {
    Number  int      `json:"number"`
    DOI     string   `json:"doi"`
    PMID    string   `json:"pmid"`
    Title   string   `json:"title"`
    Year    int      `json:"year"`
    Journal string   `json:"journal"`
    Authors []string `json:"authors"`
    DocID   string   `json:"doc_id"`
}

// ParseCitationOrder returns the unique [n] citation numbers in first-occurrence order
func ParseCitationOrder(answerText string) []int {
    re := regexp.MustCompile(`\[(\d+)\]`)
    seen := map[int]bool{}
    order := []int{}
    for _, m := range re.FindAllStringSubmatch(answerText, -1) {
        if len(m) < 2 {
            continue
        }
        // safe parse
        var n int
        fmt.Sscanf(m[1], "%d", &n)
        if n <= 0 {
            continue
        }
        if !seen[n] {
            seen[n] = true
            order = append(order, n)
        }
    }
    return order
}

// BuildBibliography builds a references list in the order of first citations; returns warnings
func BuildBibliography(answerText string, sources []SourceItem) (ordered []SourceItem, warnings []string) {
    if len(sources) == 0 {
        return nil, []string{"no sources provided"}
    }
    // index sources by number
    byNum := map[int]SourceItem{}
    nums := []int{}
    for _, s := range sources {
        byNum[s.Number] = s
        nums = append(nums, s.Number)
    }
    sort.Ints(nums)
    // ensure contiguous numbering 1..N; if not, warn but continue
    for i := 1; i <= len(nums); i++ {
        if _, ok := byNum[i]; !ok {
            warnings = append(warnings, fmt.Sprintf("source number %d missing", i))
        }
    }
    order := ParseCitationOrder(answerText)
    if len(order) == 0 {
        // fallback: use numeric order
        for i := 1; i <= len(nums); i++ {
            if s, ok := byNum[i]; ok {
                ordered = append(ordered, s)
            }
        }
        return ordered, warnings
    }
    // add in citation order first
    seen := map[int]bool{}
    for _, n := range order {
        if s, ok := byNum[n]; ok {
            if !seen[n] {
                ordered = append(ordered, s)
                seen[n] = true
            }
        } else {
            warnings = append(warnings, fmt.Sprintf("citation [%d] has no matching source", n))
        }
    }
    // append the rest (never cited but provided)
    for i := 1; i <= len(nums); i++ {
        if s, ok := byNum[i]; ok && !seen[i] {
            ordered = append(ordered, s)
        }
    }
    return ordered, warnings
}

// FormatReference renders a single source into a compact reference string
func FormatReference(s SourceItem) string {
    // Authors: join with comma; limit to 6 then et al.
    authors := strings.Join(s.Authors, ", ")
    if authors == "" {
        authors = "Unknown Authors"
    }
    year := "n.d."
    if s.Year > 0 {
        year = fmt.Sprintf("%d", s.Year)
    }
    journal := s.Journal
    if journal == "" {
        journal = ""
    }
    var tail []string
    if s.DOI != "" {
        tail = append(tail, fmt.Sprintf("doi:%s", s.DOI))
    }
    if s.PMID != "" {
        tail = append(tail, fmt.Sprintf("pmid:%s", s.PMID))
    }
    tailStr := strings.Join(tail, " ")
    if tailStr != "" {
        tailStr = " " + tailStr
    }
    title := s.Title
    if title == "" {
        title = "Untitled"
    }
    if journal != "" {
        return fmt.Sprintf("%s (%s). %s. %s.%s", authors, year, title, journal, tailStr)
    }
    return fmt.Sprintf("%s (%s). %s.%s", authors, year, title, tailStr)
}


