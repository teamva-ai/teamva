package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
)

// parseJSONCConfig accepts V2's JSON plus // comments, /* comments */ and
// trailing commas. It only standardizes those extensions; encoding/json still
// validates all JSON syntax. Blanking rather than deleting preserves offsets.
// Never return decoder text: type errors may include credential-bearing keys.
func parseJSONCConfig(payload []byte) (map[string]any, error) {
	standard := append([]byte(nil), payload...)
	inString := false
	for index := 0; index < len(standard); index++ {
		if inString {
			switch standard[index] {
			case '\\':
				index++ // An escaped quote or slash is not a delimiter.
			case '"':
				inString = false
			}
			continue
		}
		if standard[index] == '"' {
			inString = true
			continue
		}
		if standard[index] != '/' || index+1 >= len(standard) {
			continue
		}
		switch standard[index+1] {
		case '/':
			for index < len(standard) && standard[index] != '\n' && standard[index] != '\r' {
				standard[index] = ' '
				index++
			}
		case '*':
			start := index
			standard[index], standard[index+1] = ' ', ' '
			index += 2
			for index+1 < len(standard) && !(standard[index] == '*' && standard[index+1] == '/') {
				if !jsonWhitespace(standard[index]) {
					standard[index] = ' '
				}
				index++
			}
			if index+1 >= len(standard) {
				return nil, fmt.Errorf("unterminated JSONC comment at byte %d", start+1)
			}
			standard[index], standard[index+1] = ' ', ' '
			index++
		}
	}

	inString = false
	var previous byte
	for index := 0; index < len(standard); index++ {
		character := standard[index]
		if inString {
			if character == '\\' {
				index++
			} else if character == '"' {
				inString = false
				previous = '"'
			}
			continue
		}
		if character == '"' {
			inString = true
		} else if character == ',' && jsonValueEnd(previous) {
			next := index + 1
			for next < len(standard) && jsonWhitespace(standard[next]) {
				next++
			}
			if next < len(standard) && (standard[next] == '}' || standard[next] == ']') {
				standard[index] = ' '
			}
		}
		if !jsonWhitespace(character) {
			previous = character
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal(standard, &parsed); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			return nil, fmt.Errorf("invalid JSONC at byte %d", syntax.Offset)
		}
		return nil, errors.New("JSONC configuration must be a JSON object")
	}
	if parsed == nil {
		return nil, errors.New("JSONC configuration must be a JSON object")
	}
	return parsed, nil
}

func jsonWhitespace(character byte) bool {
	return character == ' ' || character == '\t' || character == '\r' || character == '\n'
}

func jsonValueEnd(character byte) bool {
	return character == '"' || character == '}' || character == ']' || character == 'e' ||
		character == 'l' || character >= '0' && character <= '9'
}
