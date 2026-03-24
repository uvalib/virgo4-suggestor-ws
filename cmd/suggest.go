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
// based on it.  Errors result in empty suggestions.
func (s *SuggestionContext) HandleSuggestionRequest() (*SuggestionResponse, error) {
	res := &SuggestionResponse{Suggestions: []Suggestion{}}

	// 1. Get existing Solr-based author suggestions
	authorRes, err := s.HandleAuthorSuggestionRequest()
	if err != nil {
		log.Printf("WARNING: Solr author suggestion failed: %v. Proceeding with KB only.", err)
		// If an error occurs, authorRes will be nil or an empty response,
		// so existingSuggestions will correctly be initialized as empty below.
	}

	existingSuggestions := []string{}
	if authorRes != nil {
		for _, sugg := range authorRes.Suggestions {
			existingSuggestions = append(existingSuggestions, sugg.Value)
		}
	}
	// 2. Optional Semantic Retrieval (Bedrock Knowledge Base)
	kbSuggestions := []string{}
	if s.svc.AIProvider != nil {
		var err error
		kbSuggestions, err = s.svc.AIProvider.Retrieve(s.parsedQuery)
		if err != nil {
			log.Printf("Knowledge Base retrieval failed: %s", err.Error())
		}
		if len(kbSuggestions) > 0 {
			log.Printf("[KB] Found authors: %v", kbSuggestions)
		}
	}

	// 3. Combine and Re-rank based on resource counts
	combinedAuthors := []string{}
	existingMap := make(map[string]bool)
	for _, as := range existingSuggestions {
		if !existingMap[as] {
			combinedAuthors = append(combinedAuthors, as)
			existingMap[as] = true
		}
	}
	for _, ks := range kbSuggestions {
		if !existingMap[ks] {
			combinedAuthors = append(combinedAuthors, ks)
			existingMap[ks] = true
		}
	}

	// Get counts and sort
	var finalSortedSuggestions []string
	counts := make(map[string]int)
	if len(combinedAuthors) > 0 {
		var err error
		counts, err = s.GetAuthorResourceCounts(combinedAuthors)
		if err != nil {
			log.Printf("WARNING: Failed to get resource counts: %v. Using original order.", err)
			finalSortedSuggestions = combinedAuthors
		} else {
			// Sort combining Solr score logic (if we had it) and count.
			// For now, primary sort is resource count descending.
			sort.Slice(combinedAuthors, func(i, j int) bool {
				countI := counts[combinedAuthors[i]]
				countJ := counts[combinedAuthors[j]]
				if countI != countJ {
					return countI > countJ
				}
				return combinedAuthors[i] < combinedAuthors[j] // alpha stable
			})
			finalSortedSuggestions = combinedAuthors
			
			// Log the top 5 after re-ranking
			limit := 5
			if len(finalSortedSuggestions) < 5 {
				limit = len(finalSortedSuggestions)
			}
			log.Printf("[RANK] Top %d authors after resource-count re-ranking: %v", limit, finalSortedSuggestions[:limit])
		}
	} else {
		finalSortedSuggestions = []string{}
	}

	// 4. Always use AI refinement if a provider is available
	if s.svc.AIProvider != nil {
		log.Printf("[DEBUG-FLOW] Starting AI refinement with %d context authors", len(finalSortedSuggestions))
		
		// Optional: Update prompt context with counts for the LLM
		// Construct a context string with counts if we have them
		authorContext := []string{}
		if len(finalSortedSuggestions) > 0 {
			for _, name := range finalSortedSuggestions {
				count := counts[name]
				if count > 0 {
					authorContext = append(authorContext, fmt.Sprintf("%s (%d resources)", name, count))
				} else {
					authorContext = append(authorContext, name)
				}
			}
		}

		aiRes, err := s.svc.AIProvider.GetSuggestions(s.parsedQuery, s.req.AIPrompt, authorContext)
		if err != nil {
			log.Printf("ERROR: AI refinement failed: %s. Falling back to simple suggestions.", err.Error())
		} else {
			// LOG 2: AI Response
			log.Printf("[DEBUG-FLOW] 2. LLM Response (Raw Suggestions): %v", aiRes.Suggestions)
			
			// 4. Verify AI suggestions
			verifiedSuggestions := []Suggestion{}
			
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
					verifiedSuggestions = append(verifiedSuggestions, Suggestion{Type: "author", Value: term})
				}
			}
			res.Suggestions = verifiedSuggestions
			return res, nil
		}
	}

	// 5. Fallback: Return combined authors directly if AI fails or is missing
	for _, s := range finalSortedSuggestions {
		res.Suggestions = append(res.Suggestions, Suggestion{Type: "author", Value: s})
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
	q := fmt.Sprintf("author_facet_f:(%s)", strings.Join(queryParts, " OR "))

	solrReq := SolrRequest{}
	solrReq.json.Params = SolrRequestParams{
		Q:          q,
		Rows:       0, // We only want facets
		Facet:      true,
		FacetField: []string{"author_facet_f"},
		FacetLimit: -1, // No limit to get all requested authors
		FacetMin:   1,
	}

	solrRes, err := s.SolrQuery(&solrReq)
	if err != nil {
		return nil, err
	}

	// Parse facet fields flat list: [name1, count1, name2, count2, ...]
	if facetData, ok := solrRes.FacetCounts.FacetFields["author_facet_f"]; ok {
		for i := 0; i < len(facetData); i += 2 {
			name, ok1 := facetData[i].(string)
			countFloat, ok2 := facetData[i+1].(float64) // JSON numbers are float64
			if ok1 && ok2 {
				counts[name] = int(countFloat)
			}
		}
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
