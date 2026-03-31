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
	Query    string `json:"query"`
	AIPrompt string `json:"aiPrompt"`
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
// based on it using a parallel Context Gathering approach.
func (s *SuggestionContext) HandleSuggestionRequest() (*SuggestionResponse, error) {
	res := &SuggestionResponse{Suggestions: []Suggestion{}}

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

	// We'll run 3 concurrent requests if available
	wg.Add(3)

	go func() {
		defer wg.Done()
		log.Printf("[ASYNC] Starting Solr context query")
		
		solrReq := SolrRequest{}
		solrReq.json.Params = SolrRequestParams{
			Q:          rawQuery,
			Rows:       5,
			Facet:      true,
			FacetLimit: 5,
			FacetMin:   1,
			FacetField: []string{"subject_facet", "author_facet"},
		}

		solrRes, err := s.SolrQuery(&solrReq)
		if err != nil {
			log.Printf("[ASYNC] Solr warning: %s", err.Error())
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
		log.Printf("[ASYNC] Finished Solr context query")
	}()

	go func() {
		defer wg.Done()
		if s.svc.AIProvider == nil {
			return
		}
		log.Printf("[ASYNC] Starting KB retrieval")
		kbResults, err := s.svc.AIProvider.Retrieve(rawQuery, 5)
		if err != nil {
			log.Printf("[ASYNC] KB warning: %s", err.Error())
			return
		}
		ctxData.KBAuthors = kbResults
		log.Printf("[ASYNC] Finished KB retrieval")
	}()

	go func() {
		defer wg.Done()
		if s.svc.AIProvider == nil {
			return
		}
		log.Printf("[ASYNC] Starting AI Query Dissection")
		dissected, err := s.svc.AIProvider.DissectQuery(rawQuery)
		if err != nil {
			log.Printf("[ASYNC] DissectQuery warning: %s", err.Error())
			return
		}
		ctxData.Dissected = dissected
		log.Printf("[ASYNC] Finished AI Query Dissection")
	}()

	// Wait for all 3 routines to finish with a suitable timeout (e.g. 3 seconds)
	// so slow backends don't hold up the entire suggestion request.
	startWait := time.Now()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Printf("[ASYNC] Phase 1 completed successfully in %v", time.Since(startWait))
	case <-time.After(3 * time.Second):
		log.Printf("[ASYNC] Phase 1 timed out after 3 seconds. Proceeding with partial gathered context.")
	}

	if s.svc.AIProvider != nil {
		startRefine := time.Now()
		aiRes, err := s.svc.AIProvider.GetSuggestions(rawQuery, s.req.AIPrompt, ctxData)
		if err != nil {
			log.Printf("ERROR: AI refinement failed: %s (took %v).", err.Error(), time.Since(startRefine))
		} else {
			log.Printf("[DEBUG-FLOW] Final AI Suggestions: didYouMean='%s', suggestions=%v (took %v)", aiRes.DidYouMean, aiRes.Suggestions, time.Since(startRefine))
			
			for _, sugg := range aiRes.Suggestions {
				trimmedName := strings.TrimSpace(sugg.Name)
				if trimmedName != "" {
					res.Suggestions = append(res.Suggestions, Suggestion{
						Type:   "author",
						Value:  trimmedName,
						Reason: sugg.Reason,
					})
				}
			}
			
			if len(res.Suggestions) > 0 {
				return res, nil
			}
			log.Printf("WARNING: AI refinement produced 0 valid suggestions. Returning empty.")
		}
	}
	
	// If AI fails/missing, return what we found in KB or Solr exactly if any.
	// But mostly we expect AI to handle this.
	for _, a := range ctxData.KBAuthors {
		res.Suggestions = append(res.Suggestions, Suggestion{Type: "author", Value: a})
	}

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
		Q:       query,
		Sort:    sugg.Params.Sort,
	}

	// Try without Qf first to allow broad search?
	// Or matches author?
	// Let's try matching the client behavior which likely just searches.
	// If we omit Qf, Solr uses default field.
	// Use the author config Qf (Query Fields) to search in the appropriate fields (e.g. phrase, phonetic)
	solrReq.json.Params.Qf = sugg.Params.Qf

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
