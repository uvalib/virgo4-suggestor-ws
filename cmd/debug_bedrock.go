package main

import (
	"context"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	sdktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func main() {
	log.Printf("Starting Bedrock Diagnostics...")

	// 1. Load AWS Config
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRetryer(func() aws.Retryer {
			return retry.NewStandard(func(o *retry.StandardOptions) {
				o.MaxAttempts = 1 // No SDK retries for diagnostics, we want to see the first error
			})
		}),
	)
	if err != nil {
		log.Fatalf("FAILED to load AWS config: %v", err)
	}

	client := bedrockruntime.NewFromConfig(cfg)
	
	// Test these models
	models := []string{
		"google.gemma-3-4b-it",
		"google.gemma-3-4b-it-v1:0",
		"nvidia.nemotron-nano-9b-v2", // Sanity check
	}

	for _, model := range models {
		log.Printf("\n--- Testing Model: %s ---", model)
		
		systemPrompt := `You are an expert academic librarian. Your goal is to provide high-quality AUTHOR name suggestions based on the user's query and the provided Background Research.

CORE BEHAVIOR:
1. CANONICAL NAMES: Always return the full, recognized name of the primary author in "Last, First" format (e.g., "Shakespeare, William").
2. DIVERSITY & MIXTURE: Provide a diverse list of 6-10 suggestions. This MUST include:
   - The primary canonical author(s) mapped from the query.
   - Relevant, specific researchers/authors found in the "Background Research" hits, even if they are secondary to the main topic.
3. QUERY ALIGNMENT: Proactively resolve partial names (e.g., "homer" should suggest "Homer", but also include secondary Greek scholars from research hits).
4. GROUNDING & FAILOVER: Even if "Background Research" is empty or contains errors, you MUST provide at least 6 canonical author suggestions based on your internal knowledge. Prioritize relevance and name similarity.

IMPORTANT RULES:
1. DO NOT use <think> tags or output internal reasoning. 
2. DO NOT output any conversational text or formatting outside of the JSON block.
3. If the query is a topic, suggest verified authors associated with that topic.
4. Each suggestion must have a 'name' (the author name) and 'reason' (a short explanation).
5. Output MUST be ONLY the raw JSON object matching the following schema. NO PREAMBLE. NO CONVERSATION. START WITH '{' AND END WITH '}'.
CRITICAL: DO NOT include any introductory text (like "Okay, let's..."), markdown formatting (like triple-backtick json), or follow-up comments.
{
  "didYouMean": "string or null",
  "suggestions": [
     { "name": "Author Name", "reason": "Why they are relevant" }
  ]
}
START RESPONSE WITH '{' AND NOTHING ELSE.`

		userPrompt := `USER QUERY: "Shakespeare"

=== BACKGROUND RESEARCH FROM CATALOG ===
Top catalog item titles: [Hamlet, Macbeth, Romeo and Juliet, King Lear]
Top catalog subjects: [English Literature, Drama, Elizabethan Era]
Top catalog authors: [Shakespeare, William, Bloom, Harold, Greenblatt, Stephen]
Direct Knowledge Base author hits: [Folger Shakespeare Library, Oxford Shakespeare]
Related terms/synonyms: [william shakespeare, bard of avon, elizabethan drama]
=========================================

INSTRUCTION: Provide 6-10 relevant AUTHOR names in 'suggestions' prioritizing the authors found in the Background Research. Output ONLY the raw JSON object. NO markdown formatting. NO comments.
`

		input := &bedrockruntime.ConverseInput{
			ModelId: aws.String(model),
			System: []sdktypes.SystemContentBlock{
				&sdktypes.SystemContentBlockMemberText{Value: systemPrompt},
			},
			Messages: []sdktypes.Message{
				{
					Role: sdktypes.ConversationRoleUser,
					Content: []sdktypes.ContentBlock{
						&sdktypes.ContentBlockMemberText{Value: userPrompt},
					},
				},
			},
			InferenceConfig: &sdktypes.InferenceConfiguration{
				MaxTokens: aws.Int32(20),
			},
		}

		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resp, err := client.Converse(ctx, input)
		cancel()
		duration := time.Since(start)

		if err != nil {
			log.Printf("RESULT: ERROR after %v", duration)
			log.Printf("DETAIL: %v", err)
		} else {
			log.Printf("RESULT: SUCCESS in %v", duration)
			output := resp.Output.(*sdktypes.ConverseOutputMemberMessage).Value
			for _, block := range output.Content {
				if text, ok := block.(*sdktypes.ContentBlockMemberText); ok {
					log.Printf("OUTPUT: %s", text.Value)
				}
			}
		}
	}
	
	log.Printf("\nDiagnostics complete.")
}
