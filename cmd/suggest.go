package main

import (
	"errors"
//	"fmt"
//	"sort"
//	"strconv"
//	"strings"
//	"time"
	"github.com/uvalib/virgo4-parser/v4parser"
)

// SuggestionContext contains data specific to this suggestion request
type SuggestionContext struct {
	svc *ServiceContext
	parser v4parser.SolrParser
	query string
}

// Suggestion contains data for a single suggestion
type Suggestion struct {
	Type  string `json:"type"`
	Value string `json:"value"`
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

// Validate ensures that the incoming query is handled by this service
func (s *SuggestionContext) Validate() error {
	if _, err := v4parser.ConvertToSolrWithParser(&s.parser, s.query); err != nil {
		return err
	}

	total := len(s.parser.FieldValues)

	// currently only handle single-keyword searches

	if total == 1 && len(s.parser.FieldValues["keyword"]) == 1 {
		return nil
	}

	return errors.New("unhandled query")
}

// HandleSuggestionRequest takes a keyword query and tries to find suggested searches
// based on it.  Errors result in empty suggestions.
func (s *SuggestionContext) HandleSuggestionRequest() *SuggestionResponse {
	res := &SuggestionResponse{ Suggestions: []Suggestion{} }

	var err error

	if err = s.Validate(); err != nil {
		return res
	}

	res.Suggestions = append(res.Suggestions, Suggestion{Type: "author", Value: "thomas hardy"})
	res.Suggestions = append(res.Suggestions, Suggestion{Type: "author", Value: "tom hardy"})

	return res
}
