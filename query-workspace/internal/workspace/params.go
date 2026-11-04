package workspace

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// cypherSegment is display text, or a parameter value standing in for a $name token.
type cypherSegment struct {
	Text, Title string
	Param       bool
}

// inlineParameters replaces $name tokens outside strings, comments and escaped identifiers with
// Cypher literals. It is for display and copying only; execution always passes real parameters.
func inlineParameters(query string, parameters map[string]any) ([]cypherSegment, int, error) {
	var segments []cypherSegment
	used := make(map[string]bool)
	last := 0
	for i := 0; i < len(query); {
		switch {
		case query[i] == '\'' || query[i] == '"' || query[i] == '`':
			i = skipQuoted(query, i)
		case strings.HasPrefix(query[i:], "//"):
			for i < len(query) && query[i] != '\n' {
				i++
			}
		case strings.HasPrefix(query[i:], "/*"):
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				i = len(query)
			} else {
				i += end + 4
			}
		case query[i] == '$' && i+1 < len(query) && isWordStart(query[i+1]):
			end := i + 1
			for end < len(query) && isWordPart(query[end]) {
				end++
			}
			name := query[i+1 : end]
			value, ok := parameters[name]
			if ok {
				literal, err := cypherLiteral(value)
				if err != nil {
					return nil, 0, fmt.Errorf("parameter $%s: %w", name, err)
				}
				if last < i {
					segments = append(segments, cypherSegment{Text: query[last:i]})
				}
				segments = append(segments, cypherSegment{
					Text: literal, Param: true, Title: fmt.Sprintf("$%s = %s (passed as a parameter)", name, literal),
				})
				used[name] = true
				last = end
			}
			i = end
		default:
			i++
		}
	}
	if last < len(query) {
		segments = append(segments, cypherSegment{Text: query[last:]})
	}
	return segments, len(used), nil
}

func skipQuoted(query string, i int) int {
	quote := query[i]
	i++
	for i < len(query) {
		switch {
		case query[i] == '\\' && quote != '`':
			i += 2
		case query[i] == quote && i+1 < len(query) && query[i+1] == quote:
			i += 2
		case query[i] == quote:
			return i + 1
		default:
			i++
		}
	}
	return len(query)
}

func joinSegments(segments []cypherSegment) string {
	var text strings.Builder
	for _, segment := range segments {
		text.WriteString(segment.Text)
	}
	return text.String()
}

// cypherLiteral renders a decoded JSON parameter value as an equivalent Cypher literal.
func cypherLiteral(value any) (string, error) {
	switch value := value.(type) {
	case nil:
		return "null", nil
	case bool:
		return strconv.FormatBool(value), nil
	case string:
		return quoteCypherString(value), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case int:
		return strconv.Itoa(value), nil
	case float64:
		if math.IsInf(value, 0) || math.IsNaN(value) {
			return "", fmt.Errorf("unsupported number %v", value)
		}
		literal := strconv.FormatFloat(value, 'g', -1, 64)
		if !strings.ContainsAny(literal, ".eE") {
			literal += ".0"
		}
		return literal, nil
	case json.Number:
		return value.String(), nil
	case []any:
		items := make([]string, len(value))
		for i, item := range value {
			literal, err := cypherLiteral(item)
			if err != nil {
				return "", err
			}
			items[i] = literal
		}
		return "[" + strings.Join(items, ", ") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		items := make([]string, len(keys))
		for i, key := range keys {
			literal, err := cypherLiteral(value[key])
			if err != nil {
				return "", err
			}
			items[i] = "`" + strings.ReplaceAll(key, "`", "``") + "`: " + literal
		}
		return "{" + strings.Join(items, ", ") + "}", nil
	}
	return "", fmt.Errorf("unsupported value of type %T", value)
}

func quoteCypherString(value string) string {
	var quoted strings.Builder
	quoted.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			quoted.WriteString(`\"`)
		case '\\':
			quoted.WriteString(`\\`)
		case '\n':
			quoted.WriteString(`\n`)
		case '\r':
			quoted.WriteString(`\r`)
		case '\t':
			quoted.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&quoted, `\u%04X`, r)
			} else {
				quoted.WriteRune(r)
			}
		}
	}
	quoted.WriteByte('"')
	return quoted.String()
}
