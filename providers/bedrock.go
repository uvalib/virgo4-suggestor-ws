package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	sdktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// BedrockProvider contains provider details using official AWS SDK
type BedrockProvider struct {
	Region          string
	Model           string
	KnowledgeBaseID string
	Config          aws.Config
	BedrockRuntime  *bedrockruntime.Client
	BedrockAgent    *bedrockagentruntime.Client
}

// NewBedrockProvider will instantiate a new AI provider using bedrock SDK
func NewBedrockProvider(model string, knowledgeBaseID string, client *http.Client) (*BedrockProvider, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		return nil, fmt.Errorf("unable to load aws sdk config: %s", err.Error())
	}

	if model == "" {
		return nil, fmt.Errorf("missing required model")
	}

	return &BedrockProvider{
		Region:          cfg.Region,
		Model:           model,
		KnowledgeBaseID: knowledgeBaseID,
		Config:          cfg,
		BedrockRuntime:  bedrockruntime.NewFromConfig(cfg),
		BedrockAgent:    bedrockagentruntime.NewFromConfig(cfg),
	}, nil
}

// Name returns the name of the provider
func (p *BedrockProvider) Name() string {
	return "bedrock"
}

// GetModel returns the specific model ID being used
func (p *BedrockProvider) GetModel() string {
	return p.Model
}

// Retrieve will query the Bedrock Knowledge Base and return relevant author metadata
func (p *BedrockProvider) Retrieve(query string, limit int) ([]string, error) {
	if p.KnowledgeBaseID == "" {
		return nil, nil
	}

	input := &bedrockagentruntime.RetrieveInput{
		KnowledgeBaseId: aws.String(p.KnowledgeBaseID),
		RetrievalQuery: &types.KnowledgeBaseQuery{
			Text: aws.String(query),
		},
		RetrievalConfiguration: &types.KnowledgeBaseRetrievalConfiguration{
			VectorSearchConfiguration: &types.KnowledgeBaseVectorSearchConfiguration{
				NumberOfResults: aws.Int32(int32(limit)),
			},
		},
	}

	resp, err := p.BedrockAgent.Retrieve(context.TODO(), input)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve from KB: %w", err)
	}

	results := []string{}
	for _, ref := range resp.RetrievalResults {
		author := ""
		if val, ok := ref.Metadata["author_name"]; ok {
			// val is bedrockagentruntime/document.Interface. 
			// Use its UnmarshalSmithyDocument method to decode to a string.
			var strVal string
			if err := val.UnmarshalSmithyDocument(&strVal); err == nil {
				author = strVal
			} else {
				author = fmt.Sprintf("%v", val)
			}
		}
		
		if author != "" {
			results = append(results, author)
		} else if ref.Content != nil && ref.Content.Text != nil {
			results = append(results, *ref.Content.Text)
		}
	}

	return results, nil
}

// GetSuggestions uses the Bedrock Converse API with Tool Use (Function Calling)
func (p *BedrockProvider) GetSuggestions(query string, customPrompt string, existingSuggestions []string) (*AIResponse, error) {
	systemPrompt := "You are a helpful academic librarian assistant. Provide search suggestions in JSON format. IMPORTANT: You have access to the UVA Author Knowledge Base. For EVERY query, you MUST first use the `retrieve_authors_from_kb` tool (Example: `{\"query\": \"Locke\", \"limit\": 10}`) to verify your suggestions against our official catalog before responding."

	userPrompt := ""
	if customPrompt == "" {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("The user is searching for: \"%s\".\n", query))
		if len(existingSuggestions) > 0 {
			sb.WriteString("Existing suggestions from our catalog:\n")
			for i, s := range existingSuggestions {
				sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, s))
			}
		}
		sb.WriteString("\nFollow these rules:\n")
		sb.WriteString("1. If the query is misspelled, provide the correction in 'didYouMean'.\n")
		sb.WriteString("2. Provide 6-10 relevant AUTHOR names in 'suggestions'.\n")
		sb.WriteString("3. Use the `retrieve_authors_from_kb` tool to find biographies and works if needed.\n")
		sb.WriteString("4. Return ONLY a JSON object with 'didYouMean' and 'suggestions' keys.\n")
		userPrompt = sb.String()
	} else {
		userPrompt = strings.ReplaceAll(customPrompt, "$QUERY", query)
		// Handle $SUGGESTIONS if needed, but tool use might replace the need for pre-calculated suggestions context
	}

	// Tool definition
	kbTool := &sdktypes.ToolMemberToolSpec{
		Value: sdktypes.ToolSpecification{
			Name:        aws.String("retrieve_authors_from_kb"),
			Description: aws.String("Search the UVA Author Knowledge Base. This database contains author names, detailed biographies, and lists of notable works. Use this to find authors related to specific topics, genres, or to disambiguate names."),
			InputSchema: &sdktypes.ToolInputSchemaMemberJson{
				Value: document.NewLazyDocument(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "The search query (e.g. author name, book title, or topic)",
						},
						"limit": map[string]interface{}{
							"type":        "integer",
							"description": "Maximum number of results to return (default 20, max 50)",
						},
					},
					"required": []string{"query"},
				}),
			},
		},
	}

	log.Printf("[AGENT] Config: model=%s, KB=%s", p.Model, p.KnowledgeBaseID)
	log.Printf("[AGENT] Start session: query='%s'", query)
	log.Printf("[AGENT] System Prompt: %s", systemPrompt)
	log.Printf("[AGENT] User Prompt: %s", userPrompt)

	messages := []sdktypes.Message{
		{
			Role: sdktypes.ConversationRoleUser,
			Content: []sdktypes.ContentBlock{
				&sdktypes.ContentBlockMemberText{Value: userPrompt},
			},
		},
	}

	// Reasoning Loop
	for attempt := 0; attempt < 5; attempt++ {
		input := &bedrockruntime.ConverseInput{
			ModelId: aws.String(p.Model),
			System: []sdktypes.SystemContentBlock{
				&sdktypes.SystemContentBlockMemberText{Value: systemPrompt},
			},
			Messages: messages,
			ToolConfig: &sdktypes.ToolConfiguration{
				Tools: []sdktypes.Tool{kbTool},
			},
		}
		// Force tool use only on the first turn to ensure verification
		if attempt == 0 {
			input.ToolConfig.ToolChoice = &sdktypes.ToolChoiceMemberAny{
				Value: sdktypes.AnyToolChoice{},
			}
		} else {
			input.ToolConfig.ToolChoice = &sdktypes.ToolChoiceMemberAuto{
				Value: sdktypes.AutoToolChoice{},
			}
		}

		log.Printf("[AGENT] Starting Converse API call (attempt %d)...", attempt+1)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resp, err := p.BedrockRuntime.Converse(ctx, input)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("converse error: %w", err)
		}
		log.Printf("[AGENT] Received Converse response. Stop reason: %v", resp.StopReason)

		output := resp.Output.(*sdktypes.ConverseOutputMemberMessage).Value
		messages = append(messages, output)

		// Check for Tool Use
		foundToolUse := false
		var toolResults []sdktypes.ContentBlock
		
		for _, block := range output.Content {
			if text, ok := block.(*sdktypes.ContentBlockMemberText); ok {
				log.Printf("[AGENT] Model Reasoning: %s", text.Value)
			}

			if toolUse, ok := block.(*sdktypes.ContentBlockMemberToolUse); ok {
				foundToolUse = true
				
				// Use UnmarshalSmithyDocument instead of json.Marshal for document.Interface
				var toolInput struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				p.UnmarshalSmithyDocument(toolUse.Value.Input, &toolInput)
				
				log.Printf("[AGENT] KB Tool Call: %s (query: '%s', limit: %d)", *toolUse.Value.Name, toolInput.Query, toolInput.Limit)
				
				var toolOutput string
				// Validation already handled by toolInput.Query check
				if *toolUse.Value.Name == "retrieve_authors_from_kb" {
					// No need to re-marshal or re-unmarshal, already in toolInput
					
					// Default and clamp limit
					if toolInput.Limit <= 0 {
						toolInput.Limit = 20
					}
					if toolInput.Limit > 50 {
						toolInput.Limit = 50
					}
					
					kbResults, err := p.Retrieve(toolInput.Query, toolInput.Limit)
					if toolInput.Query == "" {
						toolOutput = "Error: 'query' parameter is required for retrieve_authors_from_kb. Please provide an author name or book title."
						log.Printf("[AGENT] KB Tool Warning: Model sent empty query")
					} else if err != nil {
						toolOutput = fmt.Sprintf("Error retrieving from KB: %v", err)
						log.Printf("[AGENT] KB Tool Error: %v", err)
					} else {
						toolOutput = fmt.Sprintf("KB Results: [%s]", strings.Join(kbResults, ", "))
						log.Printf("[AGENT] KB Results: [%s]", strings.Join(kbResults, ", "))
					}
				}

				toolResults = append(toolResults, &sdktypes.ContentBlockMemberToolResult{
					Value: sdktypes.ToolResultBlock{
						ToolUseId: toolUse.Value.ToolUseId,
						Content: []sdktypes.ToolResultContentBlock{
							&sdktypes.ToolResultContentBlockMemberText{Value: toolOutput},
						},
					},
				})
			}
		}

		if foundToolUse {
			messages = append(messages, sdktypes.Message{
				Role:    sdktypes.ConversationRoleUser,
				Content: toolResults,
			})
			continue
		}

		// Final response
		var finalContent string
		for _, block := range output.Content {
			if text, ok := block.(*sdktypes.ContentBlockMemberText); ok {
				finalContent += text.Value
			}
		}

		// Parse JSON
		finalContent = strings.TrimSpace(finalContent)
		startIdx := strings.Index(finalContent, "{")
		if startIdx > -1 {
			endIdx := strings.LastIndex(finalContent, "}")
			finalContent = finalContent[startIdx : endIdx+1]
		}

		var aiResponse AIResponse
		if err := json.Unmarshal([]byte(finalContent), &aiResponse); err != nil {
			log.Printf("[AGENT] Final Output (Raw Text): %s", finalContent)
			return nil, fmt.Errorf("failed to parse AI response: %w (content: %s)", err, finalContent)
		}
		log.Printf("[AGENT] Final result: didYouMean='%s', suggestions=%v", aiResponse.DidYouMean, aiResponse.Suggestions)
		return &aiResponse, nil
	}

	return nil, fmt.Errorf("reached maximum tool use iterations")
}

// UnmarshalSmithyDocument is a helper to convert a smithy document.Interface to a Go struct
func (p *BedrockProvider) UnmarshalSmithyDocument(v interface{}, target interface{}) error {
	if v == nil {
		return nil
	}
	// Use type assertion to check for Unmarshal method
	if dm, ok := v.(interface{ Unmarshal(interface{}) error }); ok {
		return dm.Unmarshal(target)
	}
	return fmt.Errorf("unsupported document type: %T", v)
}
