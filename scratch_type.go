package main

import (
	"fmt"
	"reflect"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"
)

func main() {
	var r types.RetrievalResult
	fmt.Printf("%T\n", r.Metadata)
}
