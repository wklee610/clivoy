package knowledge

// EstimateTokens approximates token usage as UTF-8 byte length divided by three,
// rounded up. It is deterministic and intentionally more cautious for typical
// English prose than the common four-byte approximation. It is not a tokenizer
// and does not guarantee a model-specific token limit.
func EstimateTokens(text string) int {
	estimate := len(text) / 3
	if len(text)%3 != 0 {
		estimate++
	}
	return estimate
}
