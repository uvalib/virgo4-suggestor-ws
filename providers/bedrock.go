package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"reflect"
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

	// Final stabilized model choice for deployment
	bedrockModel := "nvidia.nemotron-nano-9b-v2"
	if model != "" {
		bedrockModel = model
	}

	return &BedrockProvider{
		Region:          cfg.Region,
		Model:           bedrockModel,
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
	systemPrompt := `You are an expert academic librarian. Your goal is to provide high-quality AUTHOR name suggestions.
IMPORTANT: For every incoming USER query, you MUST use the 'retrieve_authors_from_kb' tool to research and verify authors in our official catalog.
Research Strategy:
1. Use the 'retrieve_authors_from_kb' tool at least once per request to find verified authors.
2. If the query is a topic, search for "Famous authors of [topic]" to find relevant names.
3. Return ONLY verified authors present in the Knowledge Base results.
4. Each suggestion must have a 'name' (the author name) and 'reason' (a short explanation).`

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
		// Role Sequence & Content Count Logging
		roleSeq := ""
		var validMessages []sdktypes.Message
		for _, m := range messages {
			if len(m.Content) > 0 {
				roleSeq += fmt.Sprintf("[%v(%d)] ", m.Role, len(m.Content))
				validMessages = append(validMessages, m)
			}
		}
		log.Printf("[AGENT] Message History Roles: %s", roleSeq)

		input := &bedrockruntime.ConverseInput{
			ModelId: aws.String(p.Model),
			System: []sdktypes.SystemContentBlock{
				&sdktypes.SystemContentBlockMemberText{Value: systemPrompt},
			},
			Messages: validMessages,
			Output: &sdktypes.OutputConfig{
				TextFormat: &sdktypes.OutputFormat{
					Type: sdktypes.OutputFormatTypeJson,
					Structure: &sdktypes.OutputFormatStructureMemberJsonSchema{
						Value: sdktypes.JsonSchema{
							Name:        aws.String("author_suggestions"),
							Description: aws.String("List of verified author suggestions and spell check"),
							Schema:      p.getResponseSchema(),
						},
					},
				},
			},
		}
		
		// Only provide ToolConfig on attempt 0. 
		// If we find tool results, we'll inject them into the prompt and reset history 
		// for turn 1 to avoid multi-turn validation bugs with Nemotron/Gemma.
		if attempt == 0 {
			input.ToolConfig = &sdktypes.ToolConfiguration{
				Tools:      []sdktypes.Tool{kbTool},
				ToolChoice: &sdktypes.ToolChoiceMemberAny{},
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resp, err := p.BedrockRuntime.Converse(ctx, input)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("converse error: %w", err)
		}
		log.Printf("[AGENT] Received Converse response. Stop reason: %v", resp.StopReason)

		output := resp.Output.(*sdktypes.ConverseOutputMemberMessage).Value
		messages = p.safeAppendMessage(messages, output)

		// Process Tool Use
		foundToolUse := false
		var toolResults []sdktypes.ContentBlock
		resultsCache := make(map[string]string)
		
		for _, block := range output.Content {
			if toolUse, ok := block.(*sdktypes.ContentBlockMemberToolUse); ok {
				foundToolUse = true

				var toolInput struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				if err := p.UnmarshalSmithyDocument(toolUse.Value.Input, &toolInput); err != nil {
					log.Printf("[AGENT] KB Tool Unmarshal Error: %v", err)
					continue
				}
				
				log.Printf("[AGENT] KB Tool Call: %s (query: '%s', limit: %d)", *toolUse.Value.Name, toolInput.Query, toolInput.Limit)
				
				var toolOutput string
				if *toolUse.Value.Name == "retrieve_authors_from_kb" {
					cacheKey := fmt.Sprintf("%s:%d", toolInput.Query, toolInput.Limit)
					if cached, ok := resultsCache[cacheKey]; ok {
						toolOutput = cached
						log.Printf("[AGENT] KB Tool Cache Hit for '%s'", toolInput.Query)
					} else {
						actualLimit := toolInput.Limit
						if actualLimit <= 0 {
							actualLimit = 20
						}
						searchQuery := toolInput.Query
						if strings.TrimSpace(searchQuery) == "" {
							log.Printf("[AGENT] KB Tool Warning: Model sent empty query, defaulting to original query '%s'", query)
							searchQuery = query
						}

						kbResults, err := p.Retrieve(searchQuery, actualLimit)
						if err != nil {
							toolOutput = fmt.Sprintf("Error retrieving from KB: %v", err)
							log.Printf("[AGENT] KB Tool Error: %v", err)
						} else {
							toolOutput = fmt.Sprintf("KB Results: [%s]", strings.Join(kbResults, ", "))
							log.Printf("[AGENT] KB Results: [%s] (limit: %d)", strings.Join(kbResults, ", "), actualLimit)
						}
						resultsCache[cacheKey] = toolOutput
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
			log.Printf("[AGENT] Stability: Collapsing tool results into prompt and resetting history")
			// We take the last tool result and append it to our context
			// In a more complex agent we would handle multiple tool results, 
			// but here we just need the verified authors.
			contextUpdate := "\n\nCRITICAL KNOWLEDGE BASE RESEARCH RESULTS:\n" + toolResults[len(toolResults)-1].(*sdktypes.ContentBlockMemberToolResult).Value.Content[0].(*sdktypes.ToolResultContentBlockMemberText).Value
			
			messages = []sdktypes.Message{
				{
					Role: sdktypes.ConversationRoleUser,
					Content: []sdktypes.ContentBlock{
						&sdktypes.ContentBlockMemberText{Value: userPrompt + contextUpdate},
					},
				},
			}
			continue
		}

		// Final response
		var finalContent string
		for _, block := range output.Content {
			if text, ok := block.(*sdktypes.ContentBlockMemberText); ok {
				finalContent += text.Value
			}
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

// safeAppendMessage ensures that messages alternate roles correctly and removes empty content
func (p *BedrockProvider) safeAppendMessage(history []sdktypes.Message, msg sdktypes.Message) []sdktypes.Message {
	// 1. Sanitize content and ensure non-empty
	var validContent []sdktypes.ContentBlock
	hasToolUse := false
	
	for _, b := range msg.Content {
		if _, ok := b.(*sdktypes.ContentBlockMemberToolUse); ok {
			hasToolUse = true
		}
	}

	for _, b := range msg.Content {
		// Detect block types
		if tb, ok := b.(*sdktypes.ContentBlockMemberText); ok {
			if strings.TrimSpace(tb.Value) != "" {
				// CRITICAL: Many models on Bedrock (Gemma, Nvidia) incorrectly fail 
				// with role alternation or "streaming mode" errors if an Assistant turn 
				// contains both reasoning text AND tool calls.
				if msg.Role == sdktypes.ConversationRoleAssistant && hasToolUse {
					log.Printf("[AGENT] Safety: Stripping reasoning text from tool-use turn for stability")
					log.Printf("[AGENT] Model Reasoning: %s", tb.Value)
					continue
				}
				log.Printf("[AGENT] Model Reasoning: %s", tb.Value)
				validContent = append(validContent, b)
			}
		} else {
			validContent = append(validContent, b)
		}
	}

	if len(validContent) == 0 {
		log.Printf("[AGENT] Warning: Skipping message with no valid content (Role: %v)", msg.Role)
		return history
	}
	msg.Content = validContent

	// 2. Enforce alternation
	if len(history) > 0 {
		lastMsg := history[len(history)-1]
		if lastMsg.Role == msg.Role {
			log.Printf("[AGENT] Safety: Merging consecutive %v messages to maintain alternation", msg.Role)
			lastMsg.Content = append(lastMsg.Content, msg.Content...)
			history[len(history)-1] = lastMsg
			return history
		}
	}
	
	return append(history, msg)
}

// UnmarshalSmithyDocument is a helper to convert a smithy document.Interface to a Go struct
func (p *BedrockProvider) UnmarshalSmithyDocument(v interface{}, target interface{}) error {
	if v == nil {
		return nil
	}
	
	val := reflect.ValueOf(v)
	// Try the method we found in reflection
	method := val.MethodByName("UnmarshalSmithyDocument")
	if method.IsValid() {
		results := method.Call([]reflect.Value{reflect.ValueOf(target)})
		if len(results) > 0 && !results[0].IsNil() {
			return results[0].Interface().(error)
		}
		return nil
	}

	// Fallback to the generic Unmarshal method if it exists
	genericMethod := val.MethodByName("Unmarshal")
	if genericMethod.IsValid() {
		results := genericMethod.Call([]reflect.Value{reflect.ValueOf(target)})
		if len(results) > 0 && !results[0].IsNil() {
			return results[0].Interface().(error)
		}
		return nil
	}

	return fmt.Errorf("no unmarshal method found on %T", v)
}

// getResponseSchema returns the JSON schema for the AIResponse struct
func (p *BedrockProvider) getResponseSchema() string {
	return `{
  "type": "object",
  "properties": {
    "didYouMean": { "type": ["string", "null"] },
    "suggestions": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": { "type": "string" },
          "reason": { "type": "string" }
        },
        "required": ["name", "reason"]
      }
    }
  },
  "required": ["suggestions"]
}`
}
