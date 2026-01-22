package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
		fmt.Printf("ERROR: unable to load SDK config, %v\n", err)
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
	promptBuilder.WriteString("3. Populate 'suggestions' with 6-10 relevant AUTHORS (people or organizations) related to the query. Do NOT suggest general topics/keywords.\n")
	promptBuilder.WriteString("\nRespond in JSON format.")

	prompt := promptBuilder.String()

	// Default to Anthropic (Claude) Format
	reqBody := AnthropicRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        2000,
		System:           systemPromptContent,
		Messages: []AnthropicMessage{
			{Role: "user", Content: prompt},
		},
	}
	jsonBody, err := json.Marshal(reqBody)

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

	// Default to Anthropic
	var bedrockResp AnthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&bedrockResp); err != nil {
		return emptyResponse, fmt.Errorf("failed to decode anthropic response: %w", err)
	}
	if len(bedrockResp.Content) > 0 {
		content = bedrockResp.Content[0].Text
	}

	if content == "" {
		return emptyResponse, nil
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
