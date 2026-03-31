package providers

// AIResponse represents the structured response from the AI provider
type AIResponse struct {
	DidYouMean  string                 `json:"didYouMean"`
	Suggestions []AIResponseSuggestion `json:"suggestions"`
}

// AIResponseSuggestion contains an individual suggestion and its reason
type AIResponseSuggestion struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// DissectedQuery represents the LLM's initial analysis of a search query
type DissectedQuery struct {
	Synonyms           []string `json:"synonyms"`
	RelatedAuthors     []string `json:"relatedAuthors"`
	AlternativePhrases []string `json:"alternativePhrases"`
}

// SuggestionContextData holds the gathered research from Solr, KB, and LLM dissection
type SuggestionContextData struct {
	SolrTitles       []string
	SolrSubjectFacet []string
	SolrAuthorFacet  []string
	KBAuthors        []string
	Dissected        *DissectedQuery
}

// AIProvider defines the interface for different AI backends
type AIProvider interface {
	// DissectQuery pre-processes the query to find synonyms, alternative phrases, and immediate authors
	DissectQuery(query string) (*DissectedQuery, error)

	// GetSuggestions generates search suggestions based on the user query and gathered context
	GetSuggestions(query string, customPrompt string, suggContext SuggestionContextData) (*AIResponse, error)

	// Name returns the name of the provider (e.g. "gemini", "openai")
	Name() string

	// GetModel returns the specific model ID being used
	GetModel() string

	// Retrieve will query the provider's Knowledge Base (if supported) and return relevant metadata
	Retrieve(query string, limit int) ([]string, error)
}
