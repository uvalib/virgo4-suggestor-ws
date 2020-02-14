package main

import (
	"errors"

	"github.com/uvalib/virgo4-parser/v4parser"
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
func (s *SuggestionContext) HandleSuggestionRequest() *SuggestionResponse {
	res := &SuggestionResponse{Suggestions: []Suggestion{}}

	if err := s.ParseQuery(); err != nil {
		return res
	}

	solrReq := SolrRequest{}

	solrReq.json.Params = SolrRequestParams{
		Debug:   false,
		Start:   0,
		Rows:    10,
		Qt:      "dismax_ac",
		Fl:      []string{"phrase", "score"},
		Fq:      []string{"type:author_suggest"},
		Q:       s.parsedQuery,
		Sort:    "score desc",
		ACMatch: "true",
		ACSpell: "true",
	}

	solrRes, err := s.SolrQuery(&solrReq)
	if err != nil {
		return res
	}

	for _, doc := range solrRes.Response.Docs {
		if doc.Score > s.svc.solr.scoreThreshold {
			res.Suggestions = append(res.Suggestions, Suggestion{Type: "author", Value: doc.Phrase})
		}
	}

	return res
}
