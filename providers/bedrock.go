package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
)

type BedrockProvider struct {
	Region     string
	Model      string
	HTTPClient *http.Client
	Signer     *v4.Signer
	Config     aws.Config
}

func NewBedrockProvider(model string, client *http.Client) *BedrockProvider {
	// Load default AWS config
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Printf("ERROR: BedrockProvider: unable to load SDK config (AWS Credentials missing?): %v", err)
		return nil
	}

	if model == "" {
		model = "anthropic.claude-3-sonnet-20240229-v1:0"
	}

	return &BedrockProvider{
		Region:     cfg.Region,
		Model:      model,
		HTTPClient: client,
		Signer:     v4.NewSigner(),
		Config:     cfg,
	}
}

func (p *BedrockProvider) Name() string {
	return "bedrock"
}

func (p *BedrockProvider) GetModel() string {
	return p.Model
}

// Anthropic-specific structs for Bedrock
type AnthropicRequest struct {
	AnthropicVersion string             `json:"anthropic_version"`
	MaxTokens        int                `json:"max_tokens"`
	System           string             `json:"system,omitempty"`
	Messages         []AnthropicMessage `json:"messages"`
}

type AnthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AnthropicResponse struct {
	Content []struct {
		Text string `json:"text"`
		Type string `json:"type"`
	} `json:"content"`
}

// Gemma-specific structs
type GemmaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type GemmaMessagesRequest struct {
	Messages    []GemmaMessage `json:"messages"`
	MaxTokens   int            `json:"maxTokens,omitempty"`
	Temperature float64        `json:"temperature,omitempty"`
	TopP        float64        `json:"topP,omitempty"`
}

// NOTE: Gemma response is typically generic or follows a specific structure depending on inference profile.
// We will parse it dynamically or use a generic struct.

func (p *BedrockProvider) GetSuggestions(query string, existingSuggestions []string) (AIResponse, error) {
	var emptyResponse AIResponse

	// Construct prompts
	systemPromptContent := "You are a helpful assistant that outputs JSON."

	// User prompt construction
	var promptBuilder strings.Builder
	promptBuilder.WriteString(fmt.Sprintf("You are a helpful academic librarian assistant. The user is searching for: \"%s\".\n", query))

	if len(existingSuggestions) > 0 {
		promptBuilder.WriteString("Here are some author suggestions retrieved from our catalog:\n")
		for i, s := range existingSuggestions {
			promptBuilder.WriteString(fmt.Sprintf("%d. %s\n", i+1, s))
		}
		promptBuilder.WriteString("\nAnalyze the query and these suggestions. You may keep good suggestions, refine them, or replace them if they are not relevant.\n")
	} else {
		promptBuilder.WriteString("\nNo author suggestions were found in our catalog.\n")
	}

	promptBuilder.WriteString("1. If the query contains an OBVIOUS spelling error, set 'didYouMean' to the FULL corrected query string.\n")
	promptBuilder.WriteString("2. If the query is likely intentional, leave 'didYouMean' empty.\n")
	promptBuilder.WriteString("3. Populate 'suggestions' with 6-10 relevant AUTHORS (people or organizations) related to the query.\n")
	promptBuilder.WriteString("   - STRICTLY names of people (historians, writers) or organizations/agencies.\n")
	promptBuilder.WriteString("   - Do NOT suggest book titles, general topics, historical events, or refined search queries.\n")
	promptBuilder.WriteString("   - Example: For 'civil war', suggest 'Foote, Shelby' or 'McPherson, James', NOT 'Civil War Battles'.\n")
	promptBuilder.WriteString("\nRespond in JSON format.")

	prompt := promptBuilder.String()

	var jsonBody []byte
	var err error

	// Check Model ID for "gemma"
	modelLower := strings.ToLower(p.Model)

	if strings.Contains(modelLower, "gemma") {
		// Gemma Format
		// Combine system prompt into the first user message because Gemma often expects a single conversation flow
		// or specifically "user" / "model" roles.
		fullPrompt := fmt.Sprintf("%s\n\n%s", systemPromptContent, prompt)

		reqBody := GemmaMessagesRequest{
			Messages: []GemmaMessage{
				{Role: "user", Content: fullPrompt},
			},
			MaxTokens:   2000,
			Temperature: 0.5,
			TopP:        0.9,
		}
		jsonBody, err = json.Marshal(reqBody)

	} else {
		// Default to Anthropic (Claude) Format
		reqBody := AnthropicRequest{
			AnthropicVersion: "bedrock-2023-05-31",
			MaxTokens:        2000,
			System:           systemPromptContent,
			Messages: []AnthropicMessage{
				{Role: "user", Content: prompt},
			},
		}
		jsonBody, err = json.Marshal(reqBody)
	}

	if err != nil {
		return emptyResponse, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Construct Bedrock Runtime URL
	url := fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s/invoke", p.Region, p.Model)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return emptyResponse, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Sign the request
	creds, err := p.Config.Credentials.Retrieve(context.TODO())
	if err != nil {
		return emptyResponse, fmt.Errorf("failed to retrieve credentials: %w", err)
	}

	// Calculate payload hash
	hash := sha256.Sum256(jsonBody)
	payloadHash := hex.EncodeToString(hash[:])

	if err := p.Signer.SignHTTP(context.TODO(), creds, req, payloadHash, "bedrock", p.Region, time.Now()); err != nil {
		return emptyResponse, fmt.Errorf("failed to sign request: %w", err)
	}

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return emptyResponse, fmt.Errorf("failed to invoke bedrock: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return emptyResponse, fmt.Errorf("bedrock returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var content string

	if strings.Contains(modelLower, "gemma") {
		// Generic parse for Gemma (it varies: output.message.content or choices[0].message.content)
		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return emptyResponse, fmt.Errorf("failed to decode gemma response: %w", err)
		}

		// Try finding content in common locations
		// 1. 'choices' -> [0] -> 'message' -> 'content' (OpenAI style)
		if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]interface{}); ok {
				if msg, ok := choice["message"].(map[string]interface{}); ok {
					if c, ok := msg["content"].(string); ok {
						content = c
					}
				}
			}
		}
		// 2. 'output' -> 'message' -> 'content' (Converse output style sometimes)
		if content == "" {
			if output, ok := result["output"].(map[string]interface{}); ok {
				if msg, ok := output["message"].(map[string]interface{}); ok {
					if c, ok := msg["content"].([]interface{}); ok && len(c) > 0 {
						// Assuming content block list
						if block, ok := c[0].(map[string]interface{}); ok {
							if text, ok := block["text"].(string); ok {
								content = text
							}
						}
					}
				}
			}
		}

	} else {
		// Default to Anthropic
		var bedrockResp AnthropicResponse
		if err := json.NewDecoder(resp.Body).Decode(&bedrockResp); err != nil {
			return emptyResponse, fmt.Errorf("failed to decode anthropic response: %w", err)
		}
		if len(bedrockResp.Content) > 0 {
			content = bedrockResp.Content[0].Text
		}
	}

	if content == "" {
		return emptyResponse, fmt.Errorf("empty content from AI provider")
	}

	var aiResponse AIResponse
	// Clean up content from potential markdown markers like ```json
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```json") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimSuffix(content, "```")
	} else if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
	}

	if err := json.Unmarshal([]byte(content), &aiResponse); err != nil {
		return emptyResponse, fmt.Errorf("failed to parse generated text as JSON: %w", err)
	}

	return aiResponse, nil
}
