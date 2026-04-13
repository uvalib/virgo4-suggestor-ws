package main

import (
	"encoding/json"
	"fmt"
)

type AIResponse struct {
	DidYouMean string `json:"didYouMean"`
}

func main() {
	var res AIResponse
	data := []byte(`{"didYouMean": null}`)
	json.Unmarshal(data, &res)
	fmt.Printf("Result: '%s'\n", res.DidYouMean)
}
