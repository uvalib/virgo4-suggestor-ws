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

// DissectQuery pre-processes the query to find synonyms, alternative phrases, and immediate authors
func (p *BedrockProvider) DissectQuery(query string) (*DissectedQuery, error) {
	systemPrompt := `You are an expert librarian assisting with search query analysis.
Analyze the provided user query and identify:
1. synonyms: Alternate words for the core concepts.
2. alternativePhrases: Different ways to phrase the search.
3. relatedAuthors: Any obvious and verified famous authors closely related to this topic.
Return ONLY valid JSON matching this schema:
{
  "synonyms": ["string"],
  "alternativePhrases": ["string"],
  "relatedAuthors": ["string"]
}`

	userPrompt := fmt.Sprintf("Analyze this search query: \"%s\"", query)

	messages := []sdktypes.Message{
		{
			Role: sdktypes.ConversationRoleUser,
			Content: []sdktypes.ContentBlock{
				&sdktypes.ContentBlockMemberText{Value: userPrompt},
			},
		},
	}

	input := &bedrockruntime.ConverseInput{
		ModelId: aws.String(p.Model),
		System: []sdktypes.SystemContentBlock{
			&sdktypes.SystemContentBlockMemberText{Value: systemPrompt},
		},
		Messages: messages,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	resp, err := p.BedrockRuntime.Converse(ctx, input)
	cancel()
	
	if err != nil {
		return nil, fmt.Errorf("DissectQuery converse error: %w", err)
	}

	output := resp.Output.(*sdktypes.ConverseOutputMemberMessage).Value
	var finalContent string
	for _, block := range output.Content {
		if text, ok := block.(*sdktypes.ContentBlockMemberText); ok {
			finalContent += text.Value
		}
	}

	finalContent = p.sanitizeJSON(finalContent)
	var dissected DissectedQuery
	if err := json.Unmarshal([]byte(finalContent), &dissected); err != nil {
		return nil, fmt.Errorf("failed to parse DissectQuery response: %w (content: %s)", err, finalContent)
	}

	return &dissected, nil
}

// GetSuggestions uses the Bedrock Converse API with Tool Use (Function Calling)
func (p *BedrockProvider) GetSuggestions(query string, customPrompt string, suggContext SuggestionContextData) (*AIResponse, error) {
	systemPrompt := `You are an expert academic librarian. Your goal is to provide high-quality AUTHOR name suggestions based on the user's query and the provided Background Research.
IMPORTANT RULES:
1. DO NOT use <think> tags or output internal reasoning. 
2. DO NOT output any conversational text or formatting outside of the JSON block.
3. If the query is a topic, suggest verified authors associated with that topic.
4. Each suggestion must have a 'name' (the author name) and 'reason' (a short explanation).
5. Output MUST be ONLY the raw JSON object matching the following schema. 
CRITICAL: DO NOT include any preamble, introductory text, markdown formatting (like triple-backtick json), or follow-up comments.
{
  "didYouMean": "string or null",
  "suggestions": [
     { "name": "Author Name", "reason": "Why they are relevant" }
  ]
}`

	userPrompt := ""
	if customPrompt == "" {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("USER QUERY: \"%s\"\n\n", query))
		
		sb.WriteString("=== BACKGROUND RESEARCH FROM CATALOG ===\n")
		if len(suggContext.SolrTitles) > 0 {
			sb.WriteString(fmt.Sprintf("Top catalog item titles: %v\n", suggContext.SolrTitles))
		}
		if len(suggContext.SolrSubjectFacet) > 0 {
			sb.WriteString(fmt.Sprintf("Top catalog subjects: %v\n", suggContext.SolrSubjectFacet))
		}
		if len(suggContext.SolrAuthorFacet) > 0 {
			sb.WriteString(fmt.Sprintf("Top catalog authors: %v\n", suggContext.SolrAuthorFacet))
		}
		if len(suggContext.KBAuthors) > 0 {
			sb.WriteString(fmt.Sprintf("Direct Knowledge Base author hits: %v\n", suggContext.KBAuthors))
		}
		if suggContext.Dissected != nil {
			sb.WriteString(fmt.Sprintf("Related terms/synonyms: %v\n", suggContext.Dissected.Synonyms))
			if len(suggContext.Dissected.RelatedAuthors) > 0 {
				sb.WriteString(fmt.Sprintf("Conceptually related authors: %v\n", suggContext.Dissected.RelatedAuthors))
			}
		}
		sb.WriteString("=========================================\n\n")

		sb.WriteString("INSTRUCTION: Provide 6-10 relevant AUTHOR names in 'suggestions' prioritizing the authors found in the Background Research. Output ONLY the raw JSON object. NO markdown formatting. NO comments.\n")
		userPrompt = sb.String()
	} else {
		userPrompt = strings.ReplaceAll(customPrompt, "$QUERY", query)
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

	// Single Turn Completion (RAG Pattern)
	input := &bedrockruntime.ConverseInput{
		ModelId: aws.String(p.Model),
		System: []sdktypes.SystemContentBlock{
			&sdktypes.SystemContentBlockMemberText{Value: systemPrompt},
		},
		Messages: messages,
		InferenceConfig: &sdktypes.InferenceConfiguration{
			MaxTokens: aws.Int32(1000), // Increased to ensure the JSON is not cut off by chatty models
			Temperature: aws.Float32(0.1), // Even lower temp for more rigid, deterministic output
		},
	}
	
	startTurn := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	resp, err := p.BedrockRuntime.Converse(ctx, input)
	cancel()
	durationTurn := time.Since(startTurn)
	
	if err != nil {
		return nil, fmt.Errorf("converse error after %v: %w", durationTurn, err)
	}
	
	log.Printf("[AGENT] Completed Inference in %v", durationTurn)

	output := resp.Output.(*sdktypes.ConverseOutputMemberMessage).Value
	
	// Final response parsing
	var aiResponse AIResponse
	var finalContent string
	for _, block := range output.Content {
		if text, ok := block.(*sdktypes.ContentBlockMemberText); ok {
			finalContent += text.Value
		}
	}

	// Sanitize and Parse
	finalContent = p.sanitizeJSON(finalContent)
	if err := json.Unmarshal([]byte(finalContent), &aiResponse); err != nil {
		log.Printf("[AGENT] Final Output (Raw Text after sanitize): %s", finalContent)
		return nil, fmt.Errorf("failed to parse AI response: %w", err)
	}

	log.Printf("[AGENT] Final result: didYouMean='%s', suggestions count=%d", aiResponse.DidYouMean, len(aiResponse.Suggestions))
	return &aiResponse, nil
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

// sanitizeJSON handles common AI output issues like smart quotes and preamble text
func (p *BedrockProvider) sanitizeJSON(input string) string {
	// 0. Aggressively strip out generic <think> tags which some models dump
	for {
		startThink := strings.Index(input, "<think>")
		endThink := strings.Index(input, "</think>")
		if startThink != -1 && endThink != -1 && endThink > startThink {
			input = input[:startThink] + input[endThink+len("</think>"):]
		} else {
			break
		}
	}
	
	// Also strip if malformed trailing </think> left behind
	lastThink := strings.LastIndex(input, "</think>")
	if lastThink != -1 {
		input = input[lastThink+len("</think>"):]
	}

	// 1. Extract JSON part from markdown or surrounding text
	startIdx := strings.Index(input, "{")
	if startIdx > -1 {
		endIdx := strings.LastIndex(input, "}")
		if endIdx > startIdx {
			input = input[startIdx : endIdx+1]
		} else {
			// If we found a '{' but no closing '}', it's definitely invalid
			// or truncated. Let's still trim leading and return.
			input = input[startIdx:]
		}
	} else {
		// If no '{' is found at all, the AI response contains no JSON.
		// Return empty JSON object to prevent unmarshal errors and allow caller to handle.
		return "{}"
	}

	// 2. Replace smart/curly quotes with standard ASCII equivalents
	input = strings.ReplaceAll(input, "“", "\"")
	input = strings.ReplaceAll(input, "”", "\"")
	input = strings.ReplaceAll(input, "‘", "'")
	input = strings.ReplaceAll(input, "’", "'")

	// 3. Remove literal newlines/returns within the JSON block
	input = strings.ReplaceAll(input, "\n", " ")
	input = strings.ReplaceAll(input, "\r", " ")

	return strings.TrimSpace(input)
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
