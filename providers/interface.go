package providers

// AIResponse represents the structured response from the AI provider
type AIResponse struct {
	DidYouMean  string                 `json:"didYouMean"`
	Suggestions []AIResponseSuggestion `json:"suggestions"`
	Usage       AIUsage                `json:"usage,omitempty"`
	Reasoning   string                 `json:"reasoning,omitempty"`
	InputPrompt string                 `json:"inputPrompt,omitempty"`
	RawOutput   string                 `json:"rawOutput,omitempty"`
}

// AIUsage captures token metrics from the provider
type AIUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
}

// AIResponseSuggestion contains an individual suggestion and its reason
type AIResponseSuggestion struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// SuggestionContextData holds the gathered research from Solr and KB
type SuggestionContextData struct {
	KBAuthors []string
}

// AIProvider defines the interface for different AI backends
type AIProvider interface {

	// GetSuggestions generates search suggestions based on the user query and gathered context
	GetSuggestions(query string, customPrompt string, suggContext SuggestionContextData, debug bool, features []string) (*AIResponse, error)

	// Name returns the name of the provider (e.g. "gemini", "openai")
	Name() string

	// GetModel returns the specific model ID being used
	GetModel() string

	// Retrieve will query the provider's Knowledge Base (if supported) and return relevant metadata
	Retrieve(query string, limit int) ([]string, error)
}
