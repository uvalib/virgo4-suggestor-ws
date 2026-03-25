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
func (p *BedrockProvider) Retrieve(query string) ([]string, error) {
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
				NumberOfResults: aws.Int32(40),
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
	systemPrompt := "You are a helpful academic librarian assistant that outputs search suggestions in JSON format."

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
			Description: aws.String("Search the UVA Author Metadata Knowledge Base for biographical data and notable works."),
			InputSchema: &sdktypes.ToolInputSchemaMemberJson{
				Value: document.NewLazyDocument(map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "The search query (author name or book title)",
						},
					},
					"required": []string{"query"},
				}),
			},
		},
	}

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
			if toolUse, ok := block.(*sdktypes.ContentBlockMemberToolUse); ok {
				foundToolUse = true
				inputBytes, _ := json.Marshal(toolUse.Value.Input)
				log.Printf("[AGENT] LLM requested tool: %s with input: %s", *toolUse.Value.Name, string(inputBytes))
				
				var toolOutput string
				if *toolUse.Value.Name == "retrieve_authors_from_kb" {
					var toolInput struct {
						Query string `json:"query"`
					}
					// inputBytes already marshaled above
					json.Unmarshal(inputBytes, &toolInput)
					
					kbResults, err := p.Retrieve(toolInput.Query)
					if err != nil {
						toolOutput = fmt.Sprintf("Error retrieving from KB: %v", err)
					} else {
						toolOutput = fmt.Sprintf("KB Results: [%s]", strings.Join(kbResults, ", "))
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
			return nil, fmt.Errorf("failed to parse AI response: %w (content: %s)", err, finalContent)
		}
		return &aiResponse, nil
	}

	return nil, fmt.Errorf("reached maximum tool use iterations")
}
