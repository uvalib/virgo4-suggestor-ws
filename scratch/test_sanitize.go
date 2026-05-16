package main

import (
	"fmt"
	"regexp"
	"strings"
)

func sanitizeJSON(input string) string {
	// 0. Aggressively strip out generic <think> tags
	for {
		startThink := strings.Index(input, "<think>")
		endThink := strings.Index(input, "</think>")
		if startThink != -1 && endThink != -1 && endThink > startThink {
			input = input[:startThink] + input[endThink+len("</think>"):]
		} else {
			break
		}
	}
	
	lastThink := strings.LastIndex(input, "</think>")
	if lastThink != -1 {
		input = input[lastThink+len("</think>"):]
	}

	// 1. Find the first '{' and last '}'
	startIdx := strings.Index(input, "{")
	if startIdx > -1 {
		endIdx := strings.LastIndex(input, "}")
		if endIdx > startIdx {
			input = input[startIdx : endIdx+1]
		} else {
			input = input[startIdx:]
		}
	} else {
		return "{}"
	}

	// NEW STEP: Fix malformed markers <<...>> that are outside or partially inside quotes
	// Handle cases like: "facet": <<Name>> or "facet": <<"Name">> or "facet": "<<Name>>"
	
	// First, normalize "<<Name>>" (markers inside quotes) to "Name"
	reMarkersInQuotes := regexp.MustCompile(`"(name|reason|facet|id)":\s*"<<([^>]*?)>>"`)
	input = reMarkersInQuotes.ReplaceAllString(input, `"$1": "$2"`)

	// Then, handle markers outside quotes: "facet": <<Name>> or "facet": <<"Name">>
	// We use a more careful approach: find <<...>> and strip them, ensuring we don't double quote
	reMarkersOutside := regexp.MustCompile(`"(name|reason|facet|id)":\s*<<([^>]*?)>>`)
	input = reMarkersOutside.ReplaceAllStringFunc(input, func(m string) string {
		parts := reMarkersOutside.FindStringSubmatch(m)
		key := parts[1]
		val := parts[2]
		// Strip any existing quotes from the value, we'll re-add them in the next step
		val = strings.Trim(val, "\"")
		return fmt.Sprintf("\"%s\": \"%s\"", key, val)
	})

	// 2. Escape internal unescaped quotes in key-value pairs
	re := regexp.MustCompile(`"(name|reason|facet|id)":\s*"(.*?)"(\s*[,}])`)
	input = re.ReplaceAllStringFunc(input, func(m string) string {
		parts := re.FindStringSubmatch(m)
		if len(parts) < 4 {
			return m
		}
		key := parts[1]
		val := parts[2]
		suffix := parts[3]

		var sb strings.Builder
		for i := 0; i < len(val); i++ {
			if val[i] == '"' && (i == 0 || val[i-1] != '\\') {
				sb.WriteByte('\\')
			}
			sb.WriteByte(val[i])
		}
		return fmt.Sprintf("\"%s\": \"%s\"%s", key, sb.String(), suffix)
	})

	input = strings.ReplaceAll(input, "\n", " ")
	input = strings.ReplaceAll(input, "\r", " ")

	return strings.TrimSpace(input)
}

func main() {
	input := `{ "suggestions": [ 
		{ "name": "Dawkins, Richard", "facet": "<<Dawkins, Richard>>" },
		{ "name": "Simpson, George Gaylord", "facet": <<"Simpson, George Gaylord">> }
	] }`
	
	sanitized := sanitizeJSON(input)
	fmt.Println("INPUT:")
	fmt.Println(input)
	fmt.Println("\nSANITIZED:")
	fmt.Println(sanitized)
}
