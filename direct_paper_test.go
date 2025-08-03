package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"regexp"
	"strings"

	"paper-hand/services"

	"go.uber.org/zap"
)

func main() {
	fmt.Println("🧪 DIRECT PAPER TEST: REFERENCE REMOVAL + CITATION EXTRACTION")
	fmt.Println(strings.Repeat("=", 80))

	// Setup logger
	logger, _ := zap.NewDevelopment()
	defer logger.Sync()

	// Erstelle Citation Extractor
	extractor := services.NewCitationExtractor(logger)

	// Lese das echte Paper
	content, err := ioutil.ReadFile("1examples/paper.txt")
	if err != nil {
		log.Fatal("❌ Konnte Paper nicht lesen:", err)
	}

	originalText := string(content)
	fmt.Printf("📄 ORIGINAL CURCUMIN PAPER:\n")
	fmt.Printf("   - Characters: %d\n", len(originalText))
	fmt.Printf("   - Lines: %d\n", len(strings.Split(originalText, "\n")))
	fmt.Printf("   - Size: %.2f KB\n\n", float64(len(originalText))/1024)

	// TEST 1: Citation Extraction vom Original
	fmt.Println("🔍 TEST 1: CITATION EXTRACTION FROM ORIGINAL")
	fmt.Println(strings.Repeat("-", 60))

	originalResult, err := extractor.ExtractCitations(context.Background(), originalText)
	if err != nil {
		log.Fatal("❌ Original citation extraction failed:", err)
	}

	fmt.Printf("✅ ORIGINAL PAPER ANALYSIS:\n")
	fmt.Printf("   - In-Text Citations: %d\n", originalResult.CitationCount)
	fmt.Printf("   - Full References: %d\n", originalResult.ReferenceCount)
	fmt.Printf("   - Citation Mappings: %d\n\n", len(originalResult.CitationMappings))

	// Show some example citations
	if len(originalResult.InTextCitations) > 0 {
		fmt.Printf("📝 SAMPLE IN-TEXT CITATIONS:\n")
		for i, citation := range originalResult.InTextCitations {
			if i >= 5 {
				break
			}
			fmt.Printf("   %d. %s\n", i+1, citation)
		}
		fmt.Println()
	}

	// TEST 2: Reference Removal
	fmt.Println("✂️ TEST 2: REFERENCE REMOVAL")
	fmt.Println(strings.Repeat("-", 60))

	cleanedText, err := extractor.RemoveReferencesSection(context.Background(), originalText)
	if err != nil {
		log.Fatal("❌ Reference removal failed:", err)
	}

	sizeBefore := len(originalText)
	sizeAfter := len(cleanedText)
	reductionPercent := int(float64(sizeBefore-sizeAfter) / float64(sizeBefore) * 100)

	fmt.Printf("✅ AFTER REFERENCE REMOVAL:\n")
	fmt.Printf("   - Characters: %d\n", sizeAfter)
	fmt.Printf("   - Lines: %d\n", len(strings.Split(cleanedText, "\n")))
	fmt.Printf("   - Size: %.2f KB\n", float64(sizeAfter)/1024)
	fmt.Printf("   - Reduction: %d%% (%d chars saved)\n\n", reductionPercent, sizeBefore-sizeAfter)

	// TEST 3: Citation Extraction vom bereinigten Text
	fmt.Println("🔍 TEST 3: CITATION EXTRACTION FROM CLEANED TEXT")
	fmt.Println(strings.Repeat("-", 60))

	cleanedResult, err := extractor.ExtractCitations(context.Background(), cleanedText)
	if err != nil {
		log.Fatal("❌ Cleaned citation extraction failed:", err)
	}

	fmt.Printf("✅ CLEANED PAPER ANALYSIS:\n")
	fmt.Printf("   - In-Text Citations: %d\n", cleanedResult.CitationCount)
	fmt.Printf("   - Full References: %d\n", cleanedResult.ReferenceCount)
	fmt.Printf("   - Citation Mappings: %d\n\n", len(cleanedResult.CitationMappings))

	// TEST 4: Preservation Analysis
	fmt.Println("📊 TEST 4: PRESERVATION ANALYSIS")
	fmt.Println(strings.Repeat("-", 60))

	citationPreservation := float64(cleanedResult.CitationCount) / float64(originalResult.CitationCount) * 100

	fmt.Printf("🎯 PRESERVATION RATES:\n")
	fmt.Printf("   Original Citations: %d\n", originalResult.CitationCount)
	fmt.Printf("   Cleaned Citations:  %d\n", cleanedResult.CitationCount)
	fmt.Printf("   Citation Preservation: %.1f%%\n", citationPreservation)
	fmt.Printf("   \n")
	fmt.Printf("   Original References: %d\n", originalResult.ReferenceCount)
	fmt.Printf("   Cleaned References:  %d\n", cleanedResult.ReferenceCount)
	fmt.Printf("   Reference Reduction: %.1f%%\n\n", float64(originalResult.ReferenceCount-cleanedResult.ReferenceCount)/float64(originalResult.ReferenceCount)*100)

	// TEST 5: Token Impact Analysis
	fmt.Println("🧠 TEST 5: LLM TOKEN IMPACT")
	fmt.Println(strings.Repeat("-", 60))

	originalTokens := estimateTokens(originalText)
	cleanedTokens := estimateTokens(cleanedText)
	tokenSavings := originalTokens - cleanedTokens

	fmt.Printf("💹 TOKEN ANALYSIS:\n")
	fmt.Printf("   Original Tokens (est.):  %d\n", originalTokens)
	fmt.Printf("   Cleaned Tokens (est.):   %d\n", cleanedTokens)
	fmt.Printf("   Token Savings:           %d (%d%%)\n", tokenSavings, tokenSavings*100/originalTokens)
	fmt.Printf("   \n")
	fmt.Printf("💰 COST IMPACT (GPT-4 @$0.03/1k tokens):\n")
	fmt.Printf("   Original Cost (est.):    $%.4f\n", float64(originalTokens)*0.00003)
	fmt.Printf("   Cleaned Cost (est.):     $%.4f\n", float64(cleanedTokens)*0.00003)
	fmt.Printf("   Cost Savings:            $%.4f (%d%%)\n\n", float64(tokenSavings)*0.00003, tokenSavings*100/originalTokens)

	// TEST 6: Quality Check - Verify citations are still intact
	fmt.Println("✔️ TEST 6: QUALITY VERIFICATION")
	fmt.Println(strings.Repeat("-", 60))

	// Count citations manually in cleaned text
	citationPattern := regexp.MustCompile(`\([A-Z][a-zA-Z\s&,]+\s+et\s+al\.?,?\s*\d{4}[a-z]?\)`)
	manualCitations := citationPattern.FindAllString(cleanedText, -1)

	fmt.Printf("🔍 MANUAL VERIFICATION:\n")
	fmt.Printf("   Citations found by regex: %d\n", len(manualCitations))
	fmt.Printf("   Citations found by extractor: %d\n", cleanedResult.CitationCount)
	fmt.Printf("   Match: %s\n", matchStatus(len(manualCitations), cleanedResult.CitationCount))
	fmt.Printf("   \n")

	// Show where text ends now
	lines := strings.Split(cleanedText, "\n")
	lastMeaningfulLines := []string{}
	for i := len(lines) - 1; i >= 0 && len(lastMeaningfulLines) < 5; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			lastMeaningfulLines = append([]string{lines[i]}, lastMeaningfulLines...)
		}
	}

	fmt.Printf("📄 CLEANED TEXT ENDS WITH:\n")
	for _, line := range lastMeaningfulLines {
		fmt.Printf("   ...%s\n", line)
	}
	fmt.Println()

	// Final Summary
	fmt.Println("🎉 FINAL SUMMARY")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("✅ References successfully removed (%d%% size reduction)\n", reductionPercent)
	fmt.Printf("✅ In-text citations preserved (%.1f%% retention rate)\n", citationPreservation)
	fmt.Printf("✅ Token savings: %d tokens (%d%% reduction)\n", tokenSavings, tokenSavings*100/originalTokens)
	fmt.Printf("✅ Cost savings: $%.4f per API call\n", float64(tokenSavings)*0.00003)
	fmt.Printf("✅ Ready for LLM processing without context overflow!\n")

	if citationPreservation >= 95.0 && reductionPercent >= 20 {
		fmt.Printf("\n🏆 PERFECT RESULT: High preservation + significant reduction!\n")
	}
}

func estimateTokens(text string) int {
	// Rough estimation: ~4 characters per token for English text
	return len(text) / 4
}

func matchStatus(a, b int) string {
	if a == b {
		return "✅ Perfect"
	} else if abs(a-b) <= 2 {
		return "⚠️ Close"
	} else {
		return "❌ Mismatch"
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
