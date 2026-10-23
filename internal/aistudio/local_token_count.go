package aistudio

// EstimatedTextTokens reuses the embedded Gemma 3 SentencePiece tokenizer used
// by AI Studio's local usage fallback. It is not a model's billing token count.
func EstimatedTextTokens(text string) int64 { return localTextTokens(text) }
