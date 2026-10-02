package basispoints

// Total input already includes cache creation; only the charge category changes.
func normalizeCacheCreationAsInput(payload object) {
	visit := func(raw any) {
		usage, ok := raw.(object)
		if !ok {
			return
		}
		for _, key := range []string{"cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens", "cache_write_tokens"} {
			if _, exists := usage[key]; exists {
				usage[key] = 0
			}
		}
		for _, name := range []string{"input_tokens_details", "prompt_tokens_details"} {
			if details, ok := usage[name].(object); ok {
				for _, key := range []string{"cache_creation_tokens", "cache_write_tokens"} {
					if _, exists := details[key]; exists {
						details[key] = 0
					}
				}
			}
		}
		if creation, ok := usage["cache_creation"].(object); ok {
			for key := range creation {
				creation[key] = 0
			}
		}
	}
	visit(payload["usage"])
	if response, ok := payload["response"].(object); ok {
		visit(response["usage"])
	}
}
