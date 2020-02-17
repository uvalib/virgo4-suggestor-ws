package main

import (
	"errors"
	"log"
	"math"
	"sort"

	"github.com/uvalib/virgo4-parser/v4parser"
	"gonum.org/v1/gonum/stat"
)

// SuggestionContext contains data specific to this suggestion request
type SuggestionContext struct {
	svc         *ServiceContext
	parser      v4parser.SolrParser
	req         SuggestionRequest
	parsedQuery string
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

// InitializeSuggestion will initialize the suggestion context based on the service context
func InitializeSuggestion(svc *ServiceContext) *SuggestionContext {
	s := &SuggestionContext{}

	s.svc = svc

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
		s.parsedQuery = s.parser.FieldValues["keyword"][0]
		return nil
	}

	return errors.New("unhandled query")
}

// HandleSuggestionRequest takes a keyword query and tries to find suggested searches
// based on it.  Errors result in empty suggestions.
func (s *SuggestionContext) HandleSuggestionRequest() (*SuggestionResponse, error) {
	res := &SuggestionResponse{Suggestions: []Suggestion{}}

	if err := s.ParseQuery(); err != nil {
		return res, err
	}

	solrReq := SolrRequest{}

	solrReq.json.Params = SolrRequestParams{
		Debug:   false,
		Start:   0,
		Rows:    1000,
		DefType: "edismax",
		Fl:      []string{"phrase", "score"},
		Q:       s.parsedQuery,
		Qf:      s.svc.config.SolrQf,
		Sort:    "score desc",
	}

	solrRes, err := s.SolrQuery(&solrReq)
	if err != nil {
		return res, err
	}

	scores := []float64{}

	for _, doc := range solrRes.Response.Docs {
		//		log.Printf("%03d %03.2f %s", i, doc.Score, doc.Phrase)
		scores = append(scores, doc.Score)
	}

	sort.Float64s(scores)

	mean := stat.Mean(scores, nil)
	median := stat.Quantile(0.5, stat.Empirical, scores, nil)
	variance := stat.Variance(scores, nil)
	stddev := math.Sqrt(variance)
	cutoff := mean + 3*stddev

	log.Printf("len      : %v", len(scores))
	log.Printf("mean     : %v", mean)
	log.Printf("median   : %v", median)
	log.Printf("variance : %v", variance)
	log.Printf("stddev   : %v", stddev)
	log.Printf("cutoff   : %v", cutoff)

	for _, doc := range solrRes.Response.Docs {
		if doc.Score < cutoff || len(res.Suggestions) >= s.svc.maxSuggestions {
			break
		}
		res.Suggestions = append(res.Suggestions, Suggestion{Type: "author", Value: doc.Phrase})
	}

	log.Printf("suggest  : %v", len(res.Suggestions))

	return res, nil
}
