package providers

// AIResponse represents the structured response from the AI provider
type AIResponse struct {
	DidYouMean  string   `json:"didYouMean"`
	Suggestions []string `json:"suggestions"`
}

// AIProvider defines the interface for different AI backends
type AIProvider interface {
	// GetSuggestions generates search suggestions based on the user query and existing suggestions
	GetSuggestions(query string, customPrompt string, existingSuggestions []string) (*AIResponse, error)

	// Name returns the name of the provider (e.g. "gemini", "openai")
	Name() string

	// GetModel returns the specific model ID being used
	GetModel() string
}
