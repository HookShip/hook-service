package capabilities

import (
	"bytes"
	"encoding/json"
)

// Marshal renders a capability document as canonical bytes: two-space indent,
// no HTML escaping, and a trailing newline. This is the exact byte form emitted
// by `hookshipctl backend describe` and stored as the authoritative fixtures.
func Marshal(document Document) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
