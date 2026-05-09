package main

import (
	"fmt"
	"math"
	"sort"
)

type BookHit struct {
	Title       string
	Score       float64
	RatingCount int
}

func main() {
	results := []BookHit{
		{Title: "Obscure Book", Score: 0.6, RatingCount: 0},
		{Title: "Popular Book", Score: 0.55, RatingCount: 1000},
		{Title: "Very Popular Book", Score: 0.5, RatingCount: 100000},
	}

	for i := range results {
		hit := &results[i]
		boost := 0.15 * math.Log10(float64(hit.RatingCount)+1.0)
		oldScore := hit.Score
		hit.Score = hit.Score * (1.0 + boost)
		fmt.Printf("Boosted '%s': %.4f -> %.4f (count=%d)\n", hit.Title, oldScore, hit.Score, hit.RatingCount)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	fmt.Println("\nFinal Ranking:")
	for i, hit := range results {
		fmt.Printf("%d. %s (Score: %.4f)\n", i+1, hit.Title, hit.Score)
	}
}
