package main

import (
	"errors"
	"log"
	"math"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/uvalib/virgo4-parser/v4parser"
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
	Type  string `json:"type"`
	Value string `json:"value"`
}

// SuggestionRequest defines the format of a suggestion request
type SuggestionRequest struct {
	Query string `json:"query"`
}

// SuggestionResponse contains the full set of suggestions
type SuggestionResponse struct {
	Suggestions []Suggestion `json:"suggestions"`
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
// based on it.  Errors result in empty suggestions.
func (s *SuggestionContext) HandleSuggestionRequest() (*SuggestionResponse, error) {
	res := &SuggestionResponse{Suggestions: []Suggestion{}}

	// 1. Get existing Solr-based author suggestions
	authorRes, err := s.HandleAuthorSuggestionRequest()
	if err != nil {
		log.Printf("Author suggestion failed: %s", err.Error())
		// Continue even if author search fails, AI might still work
	}

	existingSuggestions := []string{}
	if authorRes != nil {
		for _, sugg := range authorRes.Suggestions {
			existingSuggestions = append(existingSuggestions, sugg.Value)
		}
	}

	// LOG 1: Search Context
	log.Printf("[DEBUG-FLOW] 1. Search Context (Existing Authors): %v", existingSuggestions)

	// 2. If no AI provider, just return authors
	if s.svc.AIProvider == nil {
		log.Printf("[DEBUG-FLOW] AIProvider is NIL. Skipping LLM step. (Check startup logs for initialization errors)")
		if authorRes != nil {
			res.Suggestions = authorRes.Suggestions
		}
		log.Printf("overall  : %v", len(res.Suggestions))
		return res, nil
	}

	// 3. Call AI Provider for review/refine
	// We parse the query first to ensure it's valid, but pass the raw query to AI
	if err := s.ParseQuery(); err != nil {
		// If query is invalid for Solr/Parser, still might be valid for AI?
		// The v4parser is strict (expects field names), but raw queries are fine for AI.
		// We log the error but PROCEED for AI.
		if s.verbose {
			log.Printf("Query parsing failed ('%s'), but proceeding to AI check", err.Error())
		}
	}

	aiRes, err := s.svc.AIProvider.GetSuggestions(s.req.Query, existingSuggestions)
	if err != nil {
		log.Printf("AI provider failed: %s", err.Error())
		// Fallback to existing suggestions
		if authorRes != nil {
			res.Suggestions = authorRes.Suggestions
		}
		return res, nil
	}

	// LOG 2: AI Response
	log.Printf("[DEBUG-FLOW] 2. LLM Response (Raw Suggestions): %v", aiRes.Suggestions)
	if aiRes.DidYouMean != "" {
		log.Printf("[DEBUG-FLOW]    LLM DidYouMean: %s", aiRes.DidYouMean)
	}

	// 4. Verify AI suggestions
	// We trust the "DidYouMean" from AI mostly, but suggestions should be verified
	verifiedSuggestions := []Suggestion{}

	// If DidYouMean is present, we can add it or return it.
	// The current API structure doesn't support 'didYouMean' field natively in SuggestedSearch?
	// The client expects {type, value}.
	// If didYouMean is present, maybe add it as a "spelling" or "correction" type?
	// But client might not render it.
	// For now, focusing on the 'suggestions' list.

	// Use a channel for concurrent verification
	type resultCheck struct {
		term  string
		valid bool
	}
	checkChan := make(chan resultCheck, len(aiRes.Suggestions))

	for _, term := range aiRes.Suggestions {
		go func(t string) {
			valid := s.verifySuggestionResults(t)
			checkChan <- resultCheck{term: t, valid: valid}
		}(term)
	}

	resultsMap := make(map[string]bool)
	for i := 0; i < len(aiRes.Suggestions); i++ {
		r := <-checkChan
		resultsMap[r.term] = r.valid
	}

	for _, term := range aiRes.Suggestions {
		if resultsMap[term] {
			// Force type to "author" as requested
			verifiedSuggestions = append(verifiedSuggestions, Suggestion{Type: "author", Value: term})
		}
	}

	res.Suggestions = verifiedSuggestions
	
	log.Printf("overall  : %v", len(res.Suggestions))

	return res, nil
}

// verifySuggestionResults checks if a query returns > 0 hits in Solr
func (s *SuggestionContext) verifySuggestionResults(query string) bool {
	// Re-use author params as a base but remove specifics?
	// Or try to execute a broad search.
	// We'll use the author params for now as that's what we have configured.
	sugg := s.svc.config.Suggestions.Author

	solrReq := SolrRequest{}
	solrReq.json.Params = SolrRequestParams{
		Start:   0,
		Rows:    0, // We only care about NumFound
		DefType: sugg.Params.DefType,
		// Fl:      sugg.Params.Fl, // Not needed for count
		// Fq:      sugg.Params.Fq,
		Q:    query,
		Sort: sugg.Params.Sort,
	}

	// Try without Qf first to allow broad search?
	// Or matches author?
	// Let's try matching the client behavior which likely just searches.
	// If we omit Qf, Solr uses default field.
	// solrReq.json.Params.Qf = sugg.Params.Qf

	solrRes, err := s.SolrQuery(&solrReq)
	if err != nil {
		// Log as warning but Proceed (Fail Open) if we can't verify (e.g. Solr down/unreachable).
		log.Printf("Verification skipped for '%s' due to error: %s", query, err.Error())
		return true
	}

	return solrRes.Response.NumFound > 0
}

// HandlePingRequest sends a ping request to Solr and checks the response.
func (s *SuggestionContext) HandlePingRequest() error {
	return s.SolrPing()
}
