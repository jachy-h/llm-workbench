package config

// The config file is *.jsonc: people hand-edit it and want to leave a note
// next to a setting, so `//` and `/* */` comments and trailing commas are
// accepted. The file is still written as plain JSON, which is a subset of
// JSONC — every editor and every older build keeps working.
//
// The helpers below only rewrite the few characters that encoding/json
// rejects; everything else is passed through untouched so parse errors still
// point at the right line.

// stripJSONC removes comments and trailing commas from a JSONC document.
func stripJSONC(data []byte) []byte {
	return stripTrailingCommas(stripComments(data))
}

// stripComments blanks out comments, keeping newlines so line numbers in
// encoding/json errors stay correct.
func stripComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			if c == '\\' && i+1 < len(data) {
				i++
				out = append(out, data[i])
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				out = append(out, ' ')
				i++
			}
			if i < len(data) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			out = append(out, ' ')
			for i < len(data) {
				if data[i] == '*' && i+1 < len(data) && data[i+1] == '/' {
					i++
					break
				}
				if data[i] == '\n' {
					out = append(out, '\n')
				} else {
					out = append(out, ' ')
				}
				i++
			}
		default:
			out = append(out, c)
		}
	}
	return out
}

// stripTrailingCommas blanks a comma that is followed only by whitespace and
// a closing brace or bracket, which JSON (unlike JSONC) does not allow.
func stripTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			if c == '\\' && i+1 < len(data) {
				i++
				out = append(out, data[i])
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case ',':
			if nextNonSpace(data, i+1) == '}' || nextNonSpace(data, i+1) == ']' {
				out = append(out, ' ')
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// nextNonSpace returns the first byte at or after i that is not whitespace,
// or 0 at the end of the document.
func nextNonSpace(data []byte, i int) byte {
	for ; i < len(data); i++ {
		switch data[i] {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return data[i]
		}
	}
	return 0
}
