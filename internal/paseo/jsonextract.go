package paseo

import "encoding/json"

// ExtractJSONObjects pulls every valid JSON object out of a piece of text
// (typically an agent transcript that also contains prose/reasoning). It scans
// balanced {…} (honoring strings/escapes) and tries to parse each slice — the
// ones that parse are returned as raw JSON in order of appearance. This is a
// port of PaseoClient.extractJsonObjects from the ChemCheck integration.
func ExtractJSONObjects(text string) []json.RawMessage {
	var found []json.RawMessage
	buf := []byte(text)

	for start := 0; start < len(buf); start++ {
		if buf[start] != '{' {
			continue
		}

		depth := 0
		inString := false
		escaped := false

		for i := start; i < len(buf); i++ {
			ch := buf[i]

			if inString {
				switch {
				case escaped:
					escaped = false
				case ch == '\\':
					escaped = true
				case ch == '"':
					inString = false
				}
				continue
			}

			switch ch {
			case '"':
				inString = true
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					slice := buf[start : i+1]
					if json.Valid(slice) {
						out := make(json.RawMessage, len(slice))
						copy(out, slice)
						found = append(found, out)
					}
				}
			}

			if depth == 0 && ch == '}' {
				break
			}
		}
	}

	return found
}
