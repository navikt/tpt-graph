package workspace

import "strings"

// formatCypher changes presentation only; validation, copying and execution use the original.
func formatCypher(query string) string {
	var output strings.Builder
	depth, last := 0, 0
	inReturn := false
	lineBreak := func(at, next int, indent string) {
		output.WriteString(strings.TrimRight(query[last:at], " \t\r\n"))
		if output.Len() > 0 {
			output.WriteByte('\n')
			output.WriteString(indent)
		}
		last = next
	}
	for i := 0; i < len(query); {
		switch {
		case query[i] == '\'' || query[i] == '"' || query[i] == '`':
			quote := query[i]
			i++
			for i < len(query) {
				if query[i] == '\\' && quote != '`' {
					i = min(i+2, len(query))
				} else if query[i] == quote {
					i++
					if i < len(query) && query[i] == quote {
						i++
					} else {
						break
					}
				} else {
					i++
				}
			}
		case query[i] == '/' && i+1 < len(query) && query[i+1] == '/':
			for i < len(query) && query[i] != '\n' {
				i++
			}
		case query[i] == '(' || query[i] == '[' || query[i] == '{':
			depth++
			i++
		case query[i] == ')' || query[i] == ']' || query[i] == '}':
			depth--
			i++
		case query[i] == ',' && depth == 0 && inReturn:
			i++
			next := i
			for next < len(query) && isSpace(query[next]) {
				next++
			}
			// Keep line comments attached to the expression they describe.
			if next+1 < len(query) && query[next:next+2] == "//" {
				continue
			}
			lineBreak(i, next, "       ")
			i = next
		case isWordStart(query[i]):
			start := i
			for i < len(query) && isWordPart(query[i]) {
				i++
			}
			if depth != 0 || start > 0 && (query[start-1] == '.' || query[start-1] == '$') {
				continue
			}
			word := strings.ToUpper(query[start:i])
			clause := false
			switch word {
			case "OPTIONAL", "ORDER":
				next := i
				for next < len(query) && isSpace(query[next]) {
					next++
				}
				end := next
				for end < len(query) && isWordPart(query[end]) {
					end++
				}
				second := strings.ToUpper(query[next:end])
				if word == "OPTIONAL" && second == "MATCH" || word == "ORDER" && second == "BY" {
					i, clause = end, true
				}
			case "MATCH", "WHERE", "AND", "WITH", "RETURN", "LIMIT", "SKIP", "UNWIND", "UNION":
				clause = true
			}
			if clause {
				lineBreak(start, start, "")
				if word != "AND" {
					inReturn = word == "RETURN"
				}
			}
		default:
			i++
		}
	}
	output.WriteString(query[last:])
	return output.String()
}
