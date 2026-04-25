package main

import (
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/uvalib/virgo4-parser/v4parser"
	"github.com/uvalib/virgo4-suggestor-ws/providers"
	"gonum.org/v1/gonum/stat"
)

// SuggestionContext contains data specific to this suggestion request
type SuggestionContext struct {
	svc         *ServiceContext
	parser      v4parser.SolrParser
	req         SuggestionRequest
	parsedQuery string
	verbose     bool
}

// Suggestion contains data for a single suggestion
type Suggestion struct {
	Type   string  `json:"type"`
	Value  string  `json:"value"`
	Facet  string  `json:"facet"`
	IIIFID string  `json:"iiif_id,omitempty"`
	Source string  `json:"source"`
	Reason string  `json:"reason,omitempty"`
	Score  float64 `json:"score,omitempty"`
}

// SuggestionRequest defines the format of a suggestion request
type SuggestionRequest struct {
	Query    string   `json:"query"`
	AIPrompt string   `json:"aiPrompt"`
	Debug    bool     `json:"debug"`
	Features []string `json:"features"`
}

// SuggestionResponse contains the full set of suggestions
type SuggestionResponse struct {
	DidYouMean  string              `json:"did_you_mean,omitempty"`
	Suggestions []Suggestion        `json:"suggestions"`
	Authors     []Suggestion        `json:"authors"`
	Images      []Suggestion        `json:"images"`
	Metadata    *SuggestionMetadata `json:"metadata,omitempty"`
}

// SuggestionMetadata contains timing and token usage for the suggestion process
type SuggestionMetadata struct {
	TotalTimeMS  int64 `json:"total_time_ms"`
	Cycle1TimeMS int64 `json:"cycle1_time_ms"`
	Cycle2TimeMS int64 `json:"cycle2_time_ms"`
	Cycle3TimeMS int64 `json:"cycle3_time_ms"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	InputPrompt  string  `json:"input_prompt,omitempty"`
	RawOutput    string  `json:"raw_output,omitempty"`
	Reasoning    string  `json:"reasoning,omitempty"`
	CostPer1K    float64 `json:"cost_per_1k,omitempty"`
	Model        string  `json:"model,omitempty"`
}

func calculateCostPer1K(model string, inputTokens, outputTokens int) float64 {
	var inputPer1M, outputPer1M float64

	switch model {
	case "google.gemma-3-4b-it":
		inputPer1M = 0.04
		outputPer1M = 0.08
	case "google.gemma-3-12b-it":
		inputPer1M = 0.09
		outputPer1M = 0.29
	case "google.gemma-3-27b-it":
		inputPer1M = 0.23
		outputPer1M = 0.38
	case "moonshotai.kimi-k2.5":
		inputPer1M = 0.60
		outputPer1M = 3.00
	case "nvidia.nemotron-nano-9b-v2":
		// Example placeholders based on typical nano model inference costs
		inputPer1M = 0.60
		outputPer1M = 1.70
	case "anthropic.claude-haiku-4-5-20251001-v1:0":
		inputPer1M = 1.00
		outputPer1M = 5.00
	default:
		return 0.0 // Pricing unknown for this model
	}

	// Cost for 1,000 requests based on token counts
	return (float64(inputTokens)*inputPer1M/1000000.0 + float64(outputTokens)*outputPer1M/1000000.0) * 1000.0
}

func boolOptionWithFallback(opt string, fallback bool) bool {
	var err error
	var val bool

	if val, err = strconv.ParseBool(opt); err != nil {
		val = fallback
	}

	return val
}

// InitializeSuggestion will initialize the suggestion context based on the service context
func InitializeSuggestion(svc *ServiceContext, c *gin.Context) *SuggestionContext {
	s := &SuggestionContext{}

	s.svc = svc
	s.verbose = boolOptionWithFallback(c.Query("verbose"), false)

	return s
}

// ParseQuery ensures that the incoming query is valid, and parses it
func (s *SuggestionContext) ParseQuery() error {
	if _, err := v4parser.ConvertToSolrWithParser(&s.parser, s.req.Query); err != nil {
		return err
	}

	total := len(s.parser.FieldValues)

	// currently only handle single-keyword searches

	if total == 1 && len(s.parser.FieldValues["keyword"]) == 1 {

		// and the keyword cannot be some form of a * query

		keyword := s.parser.FieldValues["keyword"][0]

		if keyword == "" || keyword == "*" {
			return errors.New("ignoring blank/* keyword query")
		}

		s.parsedQuery = keyword

		return nil
	}

	return errors.New("unhandled query")
}

// HandleAuthorSuggestionRequest takes a keyword query and tries to find suggested
// author searches based on it.  Errors result in empty suggestions.
func (s *SuggestionContext) HandleAuthorSuggestionRequest() (*SuggestionResponse, error) {
	sugg := s.svc.config.Suggestions.Author

	res := &SuggestionResponse{Suggestions: []Suggestion{}}

	if err := s.ParseQuery(); err != nil {
		return res, err
	}

	solrReq := SolrRequest{}

	solrReq.json.Params = SolrRequestParams{
		Debug:   false,
		Start:   0,
		Rows:    100,
		DefType: sugg.Params.DefType,
		Fl:      sugg.Params.Fl,
		Fq:      sugg.Params.Fq,
		Q:       s.parsedQuery,
		Qf:      sugg.Params.Qf,
		Sort:    sugg.Params.Sort,
	}

	solrRes, err := s.SolrQuery(&solrReq)
	if err != nil {
		return res, err
	}

	if len(solrRes.Response.Docs) == 0 {
		return res, nil
	}

	scores := []float64{}

	for i, doc := range solrRes.Response.Docs {
		if s.verbose == true {
			log.Printf("%03d %03.2f %s", i, doc.Score, doc.Phrase)
		}
		scores = append(scores, doc.Score)
	}

	sort.Float64s(scores)

	mean := stat.Mean(scores, nil)
	median := stat.Quantile(0.5, stat.Empirical, scores, nil)
	variance := stat.Variance(scores, nil)
	stddev := math.Sqrt(variance)
	cutoff := mean + 2*stddev

	if s.verbose == true {
		log.Printf("len      : %v", len(scores))
		log.Printf("max      : %v", solrRes.Response.Docs[0].Score)
		log.Printf("min      : %v", solrRes.Response.Docs[len(solrRes.Response.Docs)-1].Score)
		log.Printf("mean     : %v", mean)
		log.Printf("median   : %v", median)
		log.Printf("variance : %v", variance)
		log.Printf("stddev   : %v", stddev)
		log.Printf("cutoff   : %v", cutoff)
	}

	for _, doc := range solrRes.Response.Docs {
		if doc.Score < cutoff || len(res.Suggestions) >= sugg.Limit {
			break
		}

		res.Suggestions = append(res.Suggestions, Suggestion{Type: "author", Value: doc.Phrase})
	}

	if s.verbose == true {
		log.Printf("authors  : %v", len(res.Suggestions))
	}

	return res, nil
}

// HandleSuggestionRequest takes a keyword query and tries to find suggested searches
// based on it using a parallel Context Gathering approach.
func (s *SuggestionContext) HandleSuggestionRequest() (*SuggestionResponse, error) {
	var fullStart time.Time
	if s.req.Debug {
		fullStart = time.Now()
		log.Printf("[DEBUG] Debug flag is enabled for this request")
	}
	res := &SuggestionResponse{Suggestions: []Suggestion{}}

	var cycle1Time, cycle2Time int64
	var startCycle3 time.Time

	// Ensure query is parsed (though we might use the raw query for the LLM)
	if err := s.ParseQuery(); err != nil {
		if s.parsedQuery == "" {
			s.parsedQuery = s.req.Query
		}
	}

	rawQuery := s.parsedQuery
	if rawQuery == "" {
		rawQuery = s.req.Query
	}

	var ctxData providers.SuggestionContextData
	var wg sync.WaitGroup
	// Wait for all 3 routines to finish with a suitable timeout (e.g. 3 seconds)
	// so slow backends don't hold up the entire suggestion request.
	var startCycle1 time.Time
	if s.req.Debug {
		startCycle1 = time.Now()
	}
	
	// We'll run concurrent requests (KB authors and/or KB images)
	// Determine which features are requested
	hasImages := false
	hasAuthor := len(s.req.Features) == 0
	for _, f := range s.req.Features {
		if f == "images" {
			hasImages = true
		} else if f == "author" {
			hasAuthor = true
		}
	}

	if hasImages {
		wg.Add(1)
	}
	if hasAuthor {
		wg.Add(1)
	}

	if hasAuthor {
		go func() {
			defer wg.Done()
			if s.svc.AIProvider == nil {
				return
			}
			start := time.Now()
			log.Printf("[CYCLE-1] Starting KB retrieval")
			kbResults, err := s.svc.AIProvider.Retrieve(rawQuery, 20)
			if err != nil {
				log.Printf("[CYCLE-1] KB warning: %s (took %v)", err.Error(), time.Since(start))
				return
			}
			ctxData.KBAuthors = kbResults
			log.Printf("[CYCLE-1] Finished KB retrieval (took %v)", time.Since(start))
		}()
	}

	if hasImages {
		go func() {
			defer wg.Done()
			if s.svc.AIProvider == nil {
				return
			}
			start := time.Now()
			log.Printf("[CYCLE-1] Starting Image KB retrieval")
			imageResults, err := s.svc.AIProvider.RetrieveImages(rawQuery, 20)
			if err != nil {
				log.Printf("[CYCLE-1] Image KB warning: %s (took %v)", err.Error(), time.Since(start))
				return
			}
			ctxData.KBImages = imageResults
			log.Printf("[CYCLE-1] Finished Image KB retrieval (took %v)", time.Since(start))
		}()
	}


	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Use raw time.Since(startCycle1) for log messages, that's harmless
	case <-time.After(10 * time.Second):
		log.Printf("[CYCLE-1] TIMEOUT! Context gathering halted after 10s. Proceeding with partial context")
	}
	
	if s.req.Debug {
		cycle1Time = time.Since(startCycle1).Milliseconds()
	}

	var candidates []Suggestion
	var aiReqUsage providers.AIUsage
	var aiRes *providers.AIResponse
	var dymRes *providers.AIDymResponse

	if s.svc.AIProvider != nil {
		var startCycle2 time.Time
		if s.req.Debug {
			startCycle2 = time.Now()
		}

		var c2wg sync.WaitGroup
		hasDidYouMean := false
		hasAuthor := false
		
		if len(s.req.Features) == 0 {
			// Default legacy behavior: if no features specified, we do author discovery
			hasAuthor = true
		} else {
			for _, f := range s.req.Features {
				if f == "didyoumean" {
					hasDidYouMean = true
				}
				if f == "author" {
					hasAuthor = true
				}
			}
		}

		// 1. Author Suggestions Branch
		if hasAuthor {
			c2wg.Add(1)
			go func() {
				defer c2wg.Done()
				var err error
				aiRes, err = s.svc.AIProvider.GetSuggestions(rawQuery, s.req.AIPrompt, ctxData, s.req.Debug, s.req.Features)
				if err != nil {
					log.Printf("[CYCLE-2] ERROR: AI suggestions failed: %s", err.Error())
				}
			}()
		}

		// 2. DidYouMean Branch (Parallel)
		if hasDidYouMean {
			c2wg.Add(1)
			go func() {
				defer c2wg.Done()
				var err error
				dymRes, err = s.svc.AIProvider.GetDidYouMean(rawQuery, s.req.Debug)
				if err != nil {
					log.Printf("[CYCLE-2] ERROR: AI DidYouMean failed: %s", err.Error())
				}
			}()
		}

		c2wg.Wait()

		// Process Suggestions
		if aiRes != nil {
			log.Printf("[CYCLE-2] AI suggestions produced: count=%d (took %v)", len(aiRes.Suggestions), time.Since(startCycle2))
			for _, sugg := range aiRes.Suggestions {
				trimmedName := strings.TrimSpace(sugg.Name)
				if trimmedName != "" {
					candidates = append(candidates, Suggestion{
						Type:   "author",
						Value:  trimmedName,
						Facet:  sugg.Facet,
						Source: sugg.Source,
						Reason: sugg.Reason,
					})
				}
			}
			aiReqUsage.InputTokens += aiRes.Usage.InputTokens
			aiReqUsage.OutputTokens += aiRes.Usage.OutputTokens
		}

		// Process DidYouMean
		if dymRes != nil && dymRes.DidYouMean != "" && !strings.EqualFold(strings.TrimSpace(dymRes.DidYouMean), strings.TrimSpace(rawQuery)) {
			res.DidYouMean = dymRes.DidYouMean
			aiReqUsage.InputTokens += dymRes.Usage.InputTokens
			aiReqUsage.OutputTokens += dymRes.Usage.OutputTokens
			log.Printf("[CYCLE-2] AI DidYouMean produced: '%s'", res.DidYouMean)
		}

		if s.req.Debug {
			cycle2Time = time.Since(startCycle2).Milliseconds()
		}
	}
	// If AI failed or provided no results, fall back to Knowledge Base candidates
	// Only do this if authors were actually requested or if no features were specified
	if len(candidates) == 0 && hasAuthor {
		log.Printf("[CYCLE-2] No AI candidates found. Falling back to %d KB author hits.", len(ctxData.KBAuthors))
		for _, a := range ctxData.KBAuthors {
			candidates = append(candidates, Suggestion{
				Type:   "author",
				Value:  a.Name,
				Facet:  a.FacetLabel,
				Source: "kb",
				Reason: "Author's metadata aligns with your query",
			})
		}
	}

	// Always add Image candidates if requested and found
	if hasImages && len(ctxData.KBImages) > 0 {
		log.Printf("[CYCLE-2] Adding %d KB image hits.", len(ctxData.KBImages))
		for _, img := range ctxData.KBImages {
			candidates = append(candidates, Suggestion{
				Type:   "image",
				Value:  img.Title,
				Facet:  img.ID,
				IIIFID: img.IIIFID,
				Source: "kb",
				Reason: "Image matches your search query",
				Score:  img.Score,
			})
		}
	}

	if len(candidates) > 0 {
		if s.req.Debug {
			startCycle3 = time.Now()
		}
		var vwg sync.WaitGroup
		var mu sync.Mutex
		seen := make(map[string]bool)

		log.Printf("[CYCLE-3] Starting parallel verification for %d candidates...", len(candidates))
		for _, cand := range candidates {
			vwg.Add(1)
			go func(c Suggestion) {
				defer vwg.Done()
				if c.Type == "image" {
					mu.Lock()
					defer mu.Unlock()
					if !seen[c.Facet] {
						seen[c.Facet] = true
						res.Images = append(res.Images, c)
						res.Suggestions = append(res.Suggestions, c)
					}
					return
				}
				if canonical, ok := s.verifySuggestionResults(c.Value, c.Type); ok {
					mu.Lock()
					defer mu.Unlock()
					if !seen[canonical] {
						seen[canonical] = true
						c.Value = canonical // Replace candidate with exact catalog string
						c.Facet = canonical // Populate link facet with exact catalog string
						res.Authors = append(res.Authors, c)
						res.Suggestions = append(res.Suggestions, c)
					}
				}
			}(cand)
		}
		vwg.Wait()
		
		// Cap final suggestions at 8 as requested by the user
		if len(res.Suggestions) > 8 {
			res.Suggestions = res.Suggestions[:8]
		}
	}

	if s.req.Debug {
		modelUsed := s.svc.config.AI.Model
		for _, f := range s.req.Features {
			if strings.HasPrefix(f, "llm:") {
				modelUsed = strings.TrimPrefix(f, "llm:")
			}
		}

		res.Metadata = &SuggestionMetadata{
			TotalTimeMS:  time.Since(fullStart).Milliseconds(),
			Cycle1TimeMS: cycle1Time,
			Cycle2TimeMS: cycle2Time,
			Cycle3TimeMS: 0,
			InputTokens:  aiReqUsage.InputTokens,
			OutputTokens: aiReqUsage.OutputTokens,
			CostPer1K:    calculateCostPer1K(modelUsed, aiReqUsage.InputTokens, aiReqUsage.OutputTokens),
			Model:        modelUsed,
		}
		if aiRes != nil {
			res.Metadata.InputPrompt = aiRes.InputPrompt
			res.Metadata.RawOutput = aiRes.RawOutput
			res.Metadata.Reasoning = aiRes.Reasoning
		}
		if !startCycle3.IsZero() {
			res.Metadata.Cycle3TimeMS = time.Since(startCycle3).Milliseconds()
		}
	}

	log.Printf("[DEBUG] Final Response: did_you_mean='%s', suggestions=%d, metadata=%v", 
		res.DidYouMean, len(res.Suggestions), res.Metadata != nil)

	return res, nil
}

// GetAuthorResourceCounts retrieves document counts for a list of authors from Solr
func (s *SuggestionContext) GetAuthorResourceCounts(authors []string) (map[string]int, error) {
	counts := make(map[string]int)
	if len(authors) == 0 {
		return counts, nil
	}

	// Construct OR query for exact author facets
	var queryParts []string
	for _, a := range authors {
		// Escape special characters and quote
		escaped := strings.ReplaceAll(a, "\"", "\\\"")
		queryParts = append(queryParts, fmt.Sprintf("\"%s\"", escaped))
	}
	q := fmt.Sprintf("phrase:(%s)", strings.Join(queryParts, " OR "))

	solrReq := SolrRequest{}
	solrReq.json.Params = SolrRequestParams{
		Q:          q,
		Rows:       len(authors), // Fetch all requested authors
		Fl:         []string{"phrase", "count"},
		Fq:         []string{"type:author"},
	}

	solrRes, err := s.SolrQuery(&solrReq)
	if err != nil {
		return nil, err
	}

	// Parse docs to get counts
	for _, doc := range solrRes.Response.Docs {
		counts[doc.Phrase] = doc.Count
	}

	return counts, nil
}

// verifySuggestionResults checks if a suggested name exists in the autocomplete core with hits
// and returns the CANONICAL name (e.g., "Gide, Andre") if found.
func (s *SuggestionContext) verifySuggestionResults(value string, suggType string) (string, bool) {
	sugg := s.svc.config.Suggestions.Author

	// Standard filters often include count:[2 TO *] to avoid niche results.
	// For AI-suggested authors, we relax this to count:[1 TO *] because we 
	// trust the AI's relevance judgment and want to show enriched researchers.
	fq := make([]string, len(sugg.Params.Fq))
	copy(fq, sugg.Params.Fq)
	for i, f := range fq {
		if strings.Contains(f, "count:[2 TO *]") {
			fq[i] = strings.ReplaceAll(f, "count:[2 TO *]", "count:[1 TO *]")
		}
	}

	solrReq := SolrRequest{}
	// 1. Strip leading non-alphanumeric characters (like * in *Wenger, Jared)
	// These are common in the catalog but the AI won't know to suggest them.
	cleanedName := strings.TrimLeft(value, "*\"' (")

	// 2. Escape Solr special characters in the middle of the name to prevent parser errors
	// Special chars: + - && || ! ( ) { } [ ] ^ " ~ * ? : \ /
	escapedValue := cleanedName
	for _, char := range []string{"(", ")", "[", "]", "{", "}", ":", "^", "~", "*", "?", "\\", "/", "-", "+"} {
		escapedValue = strings.ReplaceAll(escapedValue, char, "\\"+char)
	}

	solrReq.json.Params = SolrRequestParams{
		Start:   0,
		Rows:    10, // Fetch up to 10 candidates to find the 'best fit'
		DefType: "edismax",
		Q:       escapedValue, // Non-quoted to allow First Last -> Last, First matching
		Qf:      sugg.Params.Qf,
		Mm:      "75%", // Relaxed from 100% to handle missing initials or minor variations
		Fl:      []string{"phrase", "count"},
		Sort:    sugg.Params.Sort,
		Fq:      fq,
	}

	// If no filters were provided in config, ensure we at least match the type
	if len(solrReq.json.Params.Fq) == 0 {
		solrReq.json.Params.Fq = []string{fmt.Sprintf("type:%s", suggType)}
	}

	solrRes, err := s.SolrQuery(&solrReq)
	if err != nil {
		// Log error but fail CLOSED -- we don't want to show suggestions we can't verify
		log.Printf("[CYCLE-3] Solr error for '%s %s': %v (Failing closed)", suggType, value, err)
		return "", false
	}

	if solrRes.Response.NumFound > 0 && len(solrRes.Response.Docs) > 0 {
		var bestCanonical string
		var bestScore float64
		var bestCount int

		for _, doc := range solrRes.Response.Docs {
			canonical := doc.Phrase
			score, ok := isSimilar(value, canonical)
			if ok {
				// If we have multiple 'ok' matches, pick the one with the highest similarity score.
				// If scores are tied, prefer the one with a higher catalog count.
				if score > bestScore || (score == bestScore && doc.Count > bestCount) {
					bestScore = score
					bestCanonical = canonical
					bestCount = doc.Count
				}
			}
		}

		if bestCanonical != "" {
			log.Printf("[CYCLE-3] Repaired '%s %s' -> '%s' (%d hits, similarity %0.2f)", suggType, value, bestCanonical, bestCount, bestScore)
			return bestCanonical, true
		}

		// If we got hits but none were similar, log the top failure for diagnostics
		topCanon := solrRes.Response.Docs[0].Phrase
		topScore, _ := isSimilar(value, topCanon)
		log.Printf("[CYCLE-3] Rejecting '%s %s' -> Top candidate '%s' has similarity too low (%0.2f)", suggType, value, topCanon, topScore)
		return "", false
	}

	log.Printf("[CYCLE-3] Rejecting '%s %s' (No catalog results found with 75%% word match)", suggType, value)
	return "", false
}

// isSimilar performs a basic similarity check between original and canonical names.
// It ensures that we don't 'repair' a name into something completely unrelated.
func isSimilar(orig, canon string) (float64, bool) {
	clean := func(s string) []string {
		// 1. Strip catalog-specific leading symbols (e.g., * in *Wenger, Jared)
		s = strings.TrimLeft(s, "*\"' ")

		// 2. Remove dates: comma followed by digits and optional dash (e.g., ", 1973-")
		reDates := regexp.MustCompile(`,?\s*\d{4}[-\d]*`)
		s = reDates.ReplaceAllString(s, "")

		// 3. Remove roles and descriptions in parentheses (e.g., "(editor)")
		reRoles := regexp.MustCompile(`\(.*?\)`)
		s = reRoles.ReplaceAllString(s, "")

		// Standardize punctuation and split
		s = strings.ReplaceAll(s, ",", " ")
		s = strings.ReplaceAll(s, ".", " ")
		return strings.Fields(strings.ToLower(s))
	}

	oWords := clean(orig)
	cWords := clean(canon)

	if len(oWords) == 0 || len(cWords) == 0 {
		return 0, false
	}

	// Basic check: see how many words from original are in canonical
	matches := 0
	for _, ow := range oWords {
		for _, cw := range cWords {
			if ow == cw {
				matches++
				break
			}
		}
	}

	// For author names, we expect high overlap regardless of 'First Last' vs 'Last, First' order.
	// threshold. Aim for at least 60% of original words found in canonical.
	score := float64(matches) / float64(len(oWords))
	return score, score >= 0.60
}

// HandlePingRequest sends a ping request to Solr and checks the response.
func (s *SuggestionContext) HandlePingRequest() error {
	return s.SolrPing()
}
