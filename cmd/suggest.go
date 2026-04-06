package main

import (
	"errors"
	"fmt"
	"log"
	"math"
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
	Type   string `json:"type"`
	Value  string `json:"value"`
	Reason string `json:"reason,omitempty"`
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
	Metadata    *SuggestionMetadata `json:"metadata,omitempty"`
}

// SuggestionMetadata contains timing and token usage for the suggestion process
type SuggestionMetadata struct {
	TotalTimeMS  int64 `json:"total_time_ms"`
	Cycle1TimeMS int64 `json:"cycle1_time_ms"`
	Cycle2TimeMS int64 `json:"cycle2_time_ms"`
	Cycle3TimeMS int64 `json:"cycle3_time_ms"`
	InputTokens  int   `json:"input_tokens"`
	OutputTokens int   `json:"output_tokens"`
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
	
	// We'll run 2 concurrent requests (Solr and KB)
	wg.Add(2)

	go func() {
		defer wg.Done()
		start := time.Now()
		log.Printf("[CYCLE-1] Starting Solr context query")
		
		baseParams := s.svc.config.Suggestions.Author.Params
		solrReq := SolrRequest{}
		solrReq.json.Params = SolrRequestParams{
			Q:          rawQuery,
			DefType:    baseParams.DefType,
			Qf:         baseParams.Qf,
			Rows:       10,
		}

		solrRes, err := s.SolrQuery(&solrReq)
		if err != nil {
			log.Printf("[CYCLE-1] Solr warning: %s (took %v)", err.Error(), time.Since(start))
			return
		}

		// Extract Titles
		for _, doc := range solrRes.Response.Docs {
			if len(doc.Title) > 0 {
				ctxData.SolrTitles = append(ctxData.SolrTitles, doc.Title[0])
			}
		}

		// Extract Facets safely
		if solrRes.FacetCounts.FacetFields != nil {
			if subjects, ok := solrRes.FacetCounts.FacetFields["subject_facet"]; ok {
				for i := 0; i < len(subjects); i += 2 {
					if subjStr, ok := subjects[i].(string); ok {
						ctxData.SolrSubjectFacet = append(ctxData.SolrSubjectFacet, subjStr)
					}
				}
			}
			if authors, ok := solrRes.FacetCounts.FacetFields["author_facet"]; ok {
				for i := 0; i < len(authors); i += 2 {
					if authStr, ok := authors[i].(string); ok {
						ctxData.SolrAuthorFacet = append(ctxData.SolrAuthorFacet, authStr)
					}
				}
			}
		}
		log.Printf("[CYCLE-1] Finished Solr context query (took %v)", time.Since(start))
	}()

	go func() {
		defer wg.Done()
		if s.svc.AIProvider == nil {
			return
		}
		start := time.Now()
		log.Printf("[CYCLE-1] Starting KB retrieval")
		kbResults, err := s.svc.AIProvider.Retrieve(rawQuery, 10)
		if err != nil {
			log.Printf("[CYCLE-1] KB warning: %s (took %v)", err.Error(), time.Since(start))
			return
		}
		ctxData.KBAuthors = kbResults
		log.Printf("[CYCLE-1] Finished KB retrieval (took %v)", time.Since(start))
	}()


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
	var aiUsage providers.AIUsage
	if s.svc.AIProvider != nil {
		var startCycle2 time.Time
		if s.req.Debug {
			startCycle2 = time.Now()
		}
		aiRes, err := s.svc.AIProvider.GetSuggestions(rawQuery, s.req.AIPrompt, ctxData, s.req.Debug, s.req.Features)
		if err != nil {
			log.Printf("[CYCLE-2] ERROR: AI refinement failed: %s (took %v).", err.Error(), time.Since(startCycle2))
		} else {
			log.Printf("[CYCLE-2] AI suggestions produced: count=%d (took %v)", len(aiRes.Suggestions), time.Since(startCycle2))
			for _, sugg := range aiRes.Suggestions {
				trimmedName := strings.TrimSpace(sugg.Name)
				if trimmedName != "" {
					candidates = append(candidates, Suggestion{Type: "author", Value: trimmedName, Reason: sugg.Reason})
				}
			}
			res.DidYouMean = aiRes.DidYouMean
			aiUsage = aiRes.Usage
		}
		if s.req.Debug {
			cycle2Time = time.Since(startCycle2).Milliseconds()
		}
	}
	
	// If AI failed or provided no results, fall back to Knowledge Base candidates
	if len(candidates) == 0 {
		log.Printf("[CYCLE-2] No AI candidates found. Falling back to %d KB author hits.", len(ctxData.KBAuthors))
		for _, a := range ctxData.KBAuthors {
			candidates = append(candidates, Suggestion{
				Type:   "author", 
				Value:  a,
				Reason: "Author's metadata aligns with your query",
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
				if canonical, ok := s.verifySuggestionResults(c.Value, c.Type); ok {
					mu.Lock()
					defer mu.Unlock()
					if !seen[canonical] {
						seen[canonical] = true
						c.Value = canonical // Replace candidate with exact catalog string
						res.Suggestions = append(res.Suggestions, c)
					}
				}
			}(cand)
		}
		vwg.Wait()
	}

	if s.req.Debug {
		res.Metadata = &SuggestionMetadata{
			TotalTimeMS:  time.Since(fullStart).Milliseconds(),
			Cycle1TimeMS: cycle1Time,
			Cycle2TimeMS: cycle2Time,
			Cycle3TimeMS: 0,
			InputTokens:  aiUsage.InputTokens,
			OutputTokens: aiUsage.OutputTokens,
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

	solrReq := SolrRequest{}
	solrReq.json.Params = SolrRequestParams{
		Start:   0,
		Rows:    1, // We only need the top canonical match
		DefType: sugg.Params.DefType,
		Q:       value,
		Qf:      sugg.Params.Qf,
		Mm:      "100%", // Require all words from AI to be present in Solr doc
		Fl:      []string{"phrase", "count"},
		Sort:    sugg.Params.Sort,
		Fq:      sugg.Params.Fq, // Include mandatory filters like type:author and count:[2 TO *]
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
		canonical := solrRes.Response.Docs[0].Phrase
		
		// Guard against overly loose 'repairs' by checking word overlap
		if isSimilar(value, canonical) {
			log.Printf("[CYCLE-3] Repaired '%s %s' -> '%s' (%d catalog hits)", suggType, value, canonical, solrRes.Response.Docs[0].Count)
			return canonical, true
		}
		log.Printf("[CYCLE-3] Rejecting '%s %s' -> '%s' (Similarity too low)", suggType, value, canonical)
		return "", false
	}

	log.Printf("[CYCLE-3] Rejecting '%s %s' (No canonical name found with 100%% word match)", suggType, value)
	return "", false
}

// isSimilar performs a basic similarity check between original and canonical names.
// It ensures that we don't 'repair' a name into something completely unrelated.
func isSimilar(orig, canon string) bool {
	clean := func(s string) []string {
		// Remove commas and periods (for initials) then lowercase and split
		s = strings.ReplaceAll(s, ",", " ")
		s = strings.ReplaceAll(s, ".", " ")
		return strings.Fields(strings.ToLower(s))
	}

	oWords := clean(orig)
	cWords := clean(canon)

	if len(oWords) == 0 || len(cWords) == 0 {
		return false
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
	// Aim for at least 70% of original words found in canonical.
	threshold := float64(len(oWords)) * 0.70
	return float64(matches) >= threshold
}

// HandlePingRequest sends a ping request to Solr and checks the response.
func (s *SuggestionContext) HandlePingRequest() error {
	return s.SolrPing()
}
