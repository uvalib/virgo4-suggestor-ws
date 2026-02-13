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
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
)

// BedrockProvider contains provider details
type BedrockProvider struct {
	Region     string
	Model      string
	HTTPClient *http.Client
	Signer     *v4.Signer
	Config     aws.Config
}

// NewBedrockProvider will instantiate a new AI provider using bedrock
func NewBedrockProvider(model string, client *http.Client) (*BedrockProvider, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		return nil, fmt.Errorf("unable to load aws sdk config: %s", err.Error())
	}

	if model == "" {
		return nil, fmt.Errorf("missing required model")
	}

	return &BedrockProvider{
		Region:     cfg.Region,
		Model:      model,
		HTTPClient: client,
		Signer:     v4.NewSigner(),
		Config:     cfg,
	}, nil
}

// Name returns the name of the provider (e.g. "gemini", "openai")
func (p *BedrockProvider) Name() string {
	return "bedrock"
}

// GetModel returns the specific model ID being used
func (p *BedrockProvider) GetModel() string {
	return p.Model
}

// Anthropic-specific structs for Bedrock
type anthropicRequest struct {
	AnthropicVersion string             `json:"anthropic_version"`
	MaxTokens        int                `json:"max_tokens"`
	System           string             `json:"system,omitempty"`
	Messages         []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Text string `json:"text"`
		Type string `json:"type"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Gemma-specific structs
type gemmaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type gemmaMessagesRequest struct {
	Messages    []gemmaMessage `json:"messages"`
	MaxTokens   int            `json:"maxTokens,omitempty"`
	Temperature float64        `json:"temperature,omitempty"`
	TopP        float64        `json:"topP,omitempty"`
}

// GetSuggestions will take a user query, optional prompt and existing author suggestions and refine the results using
// NOTE: Gemma response is typically generic or follows a specific structure depending on inference profile.
// We will parse it dynamically or use a generic struct.
func (p *BedrockProvider) GetSuggestions(query string, customPrompt string, existingSuggestions []string) (*AIResponse, error) {
	// Construct prompts
	systemPromptContent := "You are a helpful assistant that outputs JSON."

	resultsString := "\nNo author suggestions were found in our catalog.\n"
	if len(existingSuggestions) > 0 {
		resultsString = "Here are some author suggestions retrieved from our catalog:\n"
		for i, s := range existingSuggestions {
			resultsString += fmt.Sprintf("%d. %s\n", i+1, s)
		}
	}

	prompt := ""
	if customPrompt == "" {
		log.Printf("INFO: use default prompt to get suggestions")
		var promptBuilder strings.Builder
		fmt.Fprintf(&promptBuilder, "You are a helpful academic librarian assistant. The user is searching for: \"%s\".\n", query)

		if len(existingSuggestions) > 0 {
			promptBuilder.WriteString(resultsString)
			promptBuilder.WriteString("\nAnalyze the query and these suggestions. You may keep good suggestions, refine them, or replace them if they are not relevant.\n")
		} else {
			promptBuilder.WriteString("\nNo author suggestions were found in our catalog.\n")
		}

		promptBuilder.WriteString("1. If the query contains an OBVIOUS spelling error, set 'didYouMean' to the FULL corrected query string.\n")
		promptBuilder.WriteString("2. If the query is likely intentional, leave 'didYouMean' empty.\n")
		promptBuilder.WriteString("3. Populate 'suggestions' with 6-10 relevant AUTHORS (people or organizations) related to the query.\n")
		promptBuilder.WriteString("   - STRICTLY names of people (historians, writers) or organizations/agencies.\n")
		promptBuilder.WriteString("   - Do NOT suggest book titles, general topics, historical events, or refined search queries.\n")
		promptBuilder.WriteString("   - Order the authors by relevance to the query.\n")
		promptBuilder.WriteString("   - Example: For 'civil war', suggest 'Foote, Shelby' or 'McPherson, James', NOT 'Civil War Battles'.\n")
		promptBuilder.WriteString("\nRespond in JSON format.")

		prompt = promptBuilder.String()
	} else {
		log.Printf("INFO: use custom prompt to get suggestions")
		prompt = customPrompt
		prompt = strings.Replace(prompt, "$QUERY", query, 1)
		prompt = strings.Replace(prompt, "$RESULTS", resultsString, 1)
	}

	var jsonBody []byte
	var err error

	// Check Model ID for "gemma"
	modelLower := strings.ToLower(p.Model)

	if strings.Contains(modelLower, "gemma") {
		log.Printf("INFO: prompt [%s]", prompt)

		// Gemma Format
		// Combine system prompt into the first user message because Gemma often expects a single conversation flow
		// or specifically "user" / "model" roles.
		fullPrompt := fmt.Sprintf("%s\n\n%s", systemPromptContent, prompt)

		reqBody := gemmaMessagesRequest{
			Messages: []gemmaMessage{
				{Role: "user", Content: fullPrompt},
			},
			MaxTokens:   2000,
			Temperature: 0.5,
			TopP:        0.9,
		}
		jsonBody, err = json.Marshal(reqBody)

	} else {
		// Default to Anthropic (Claude) Format
		reqBody := anthropicRequest{
			AnthropicVersion: "bedrock-2023-05-31",
			MaxTokens:        2000,
			System:           systemPromptContent,
			Messages: []anthropicMessage{
				{Role: "user", Content: prompt},
			},
		}
		jsonBody, err = json.Marshal(reqBody)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Construct Bedrock Runtime URL
	url := fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s/invoke", p.Region, p.Model)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Sign the request
	creds, err := p.Config.Credentials.Retrieve(context.TODO())
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve credentials: %w", err)
	}

	// Calculate payload hash
	hash := sha256.Sum256(jsonBody)
	payloadHash := hex.EncodeToString(hash[:])

	if err := p.Signer.SignHTTP(context.TODO(), creds, req, payloadHash, "bedrock", p.Region, time.Now()); err != nil {
		return nil, fmt.Errorf("failed to sign request: %w", err)
	}

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to invoke bedrock: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bedrock returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var content string

	if strings.Contains(modelLower, "gemma") {
		log.Printf("INFO: pull ai results from a gemma model")

		// Generic parse for Gemma (it varies: output.message.content or choices[0].message.content)
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("failed to decode gemma response: %w", err)
		}

		// Try finding content in common locations
		// 1. 'choices' -> [0] -> 'message' -> 'content' (OpenAI style)
		if choices, ok := result["choices"].([]any); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				if msg, ok := choice["message"].(map[string]any); ok {
					if c, ok := msg["content"].(string); ok {
						content = c
					}
				}
			}
		}
		// 2. 'output' -> 'message' -> 'content' (Converse output style sometimes)
		if content == "" {
			if output, ok := result["output"].(map[string]any); ok {
				if msg, ok := output["message"].(map[string]any); ok {
					if c, ok := msg["content"].([]any); ok && len(c) > 0 {
						// Assuming content block list
						if block, ok := c[0].(map[string]any); ok {
							if text, ok := block["text"].(string); ok {
								content = text
							}
						}
					}
				}
			}
		}

		// Try to extract usage for Gemma (OpenAI style)
		if usage, ok := result["usage"].(map[string]any); ok {
			in := 0
			out := 0
			if v, ok := usage["prompt_tokens"].(float64); ok {
				in = int(v)
			}
			if v, ok := usage["completion_tokens"].(float64); ok {
				out = int(v)
			}
			log.Printf("[BEDROCK-USAGE] Model: %s | Input Tokens: %d | Output Tokens: %d", p.Model, in, out)
		}

	} else {
		log.Printf("INFO: pull anthropic ai results")
		// Default to Anthropic
		var bedrockResp anthropicResponse
		if err := json.NewDecoder(resp.Body).Decode(&bedrockResp); err != nil {
			return nil, fmt.Errorf("failed to decode anthropic response: %w", err)
		}
		if len(bedrockResp.Content) > 0 {
			content = bedrockResp.Content[0].Text
		}
		log.Printf("[BEDROCK-USAGE] Model: %s | Input Tokens: %d | Output Tokens: %d", p.Model, bedrockResp.Usage.InputTokens, bedrockResp.Usage.OutputTokens)
	}

	if content == "" {
		return nil, fmt.Errorf("empty content from AI provider")
	}

	// get rid of anything before and after the { and } that bracket a json response
	content = strings.TrimSpace(content)
	startIdx := strings.Index(content, "{")
	endIdx := strings.LastIndex(content, "}")
	content = content[startIdx : endIdx+1]
	log.Printf("INFO: final ai response %s", content)

	var aiResponse AIResponse
	if err := json.Unmarshal([]byte(content), &aiResponse); err != nil {
		return nil, fmt.Errorf("failed to parse generated text as JSON: %w", err)
	}

	return &aiResponse, nil
}
