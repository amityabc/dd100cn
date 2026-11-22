package savejson

import "encoding/json"

// Marshal preserves the declared property order and System.Text.Json string
// escapes, including a stored JSON document embedded inside another receipt.
func Marshal(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	output := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); {
		if raw[i] != '"' {
			output = append(output, raw[i])
			i++
			continue
		}
		start := i
		i++
		for raw[i] != '"' {
			if raw[i] == '\\' {
				i++
			}
			i++
		}
		i++
		var s string
		if err := json.Unmarshal(raw[start:i], &s); err != nil {
			return nil, err
		}
		encoded, err := String(s).MarshalJSON()
		if err != nil {
			return nil, err
		}
		output = append(output, encoded...)
	}
	return output, nil
}
