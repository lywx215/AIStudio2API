package api

import "github.com/Mag1cFall/AIStudio2API/internal/aistudio"

// Preserve metadata-only frames and copy their storage: streamed snapshots may
// add URLs or update an earlier retrieval outcome. Never infer this evidence
// from prompt URLs, a model's answer or search grounding chunks.
func mergeURLContextMetadata(previous, next *aistudio.URLContextMetadata) *aistudio.URLContextMetadata {
	merged := &aistudio.URLContextMetadata{URLs: []aistudio.URLMetadata{}}
	if previous != nil {
		merged.URLs = append(merged.URLs, previous.URLs...)
	}
	for _, entry := range next.URLs {
		found := false
		if entry.RetrievedURL != "" {
			for index := range merged.URLs {
				if merged.URLs[index].RetrievedURL == entry.RetrievedURL {
					merged.URLs[index] = entry
					found = true
					break
				}
			}
		}
		if !found {
			merged.URLs = append(merged.URLs, entry)
		}
	}
	return merged
}

func geminiURLContextMetadata(metadata aistudio.URLContextMetadata) map[string]any {
	entries := make([]map[string]any, 0, len(metadata.URLs))
	for _, entry := range metadata.URLs {
		value := map[string]any{"retrievedUrl": entry.RetrievedURL}
		if entry.RetrievalStatus != "" {
			value["urlRetrievalStatus"] = entry.RetrievalStatus
		}
		entries = append(entries, value)
	}
	return map[string]any{"urlMetadata": entries}
}
