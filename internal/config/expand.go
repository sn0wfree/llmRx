package config

import (
	"fmt"
	"os"
	"strings"
)

// Expand replaces ${VAR} and ${VAR:-default} occurrences in s
// with the value of the corresponding environment variable. The
// feature lets operators keep secrets out of config files:
//
//	database:
//	  dsn: sqlite:///${DATA_DIR}/llmrx.db
//	tokens:
//	  - key: ${DEEPSEEK_API_KEY}
//
// Substitution rules (POSIX-ish, subset of bash):
//
//   - ${VAR}        → os.Getenv("VAR"); empty if unset
//   - ${VAR:-X}     → os.Getenv("VAR"); X if unset OR empty
//   - $$            → literal '$' (escape so config can contain $)
//   - \${VAR}       → literal "${VAR}" (escape so config can opt out)
//
// Unknown variables without a default expand to the empty string
// — YAML consumers see the same effect as omitting the field.
// Use ${VAR:?error message} for a hard fail (returns an error)
// when the variable is unset or empty.
//
// The function never panics on malformed input; an unterminated
// ${ leaves the substring verbatim so the operator can spot the
// bug in their config.
func Expand(s string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		// Escape sequences: $$ and \$ (skip the backslash, emit the char).
		if c == '\\' && i+1 < len(s) && (s[i+1] == '$' || s[i+1] == '{') {
			out.WriteByte(s[i+1])
			i += 2
			continue
		}
		if c == '$' && i+1 < len(s) {
			// $$  → literal '$'
			if s[i+1] == '$' {
				out.WriteByte('$')
				i += 2
				continue
			}
			// ${...} form
			if s[i+1] == '{' {
				end := strings.IndexByte(s[i+2:], '}')
				if end < 0 {
					// Unterminated ${ — emit verbatim.
					out.WriteString(s[i:])
					return out.String(), nil
				}
				expr := s[i+2 : i+2+end]
				val, err := resolveExpr(expr)
				if err != nil {
					return "", err
				}
				out.WriteString(val)
				i += 2 + end + 1
				continue
			}
		}
		out.WriteByte(c)
		i++
	}
	return out.String(), nil
}

// resolveExpr evaluates the inside of ${...}:
//   - NAME           → os.Getenv("NAME") (empty if unset)
//   - NAME:-DEFAULT  → os.Getenv("NAME"); DEFAULT if unset or empty
//   - NAME:?MSG      → os.Getenv("NAME"); error MSG if unset or empty
//   - NAME:+ALT      → ALT if NAME is set, empty otherwise
func resolveExpr(expr string) (string, error) {
	for _, op := range []string{":-", ":?", ":+"} {
		if i := strings.Index(expr, op); i >= 0 {
			name := expr[:i]
			rest := expr[i+2:]
			val := os.Getenv(name)
			switch op {
			case ":-":
				if val == "" {
					return rest, nil
				}
				return val, nil
			case ":?":
				if val == "" {
					if rest == "" {
						return "", fmt.Errorf("config: required env var %q is unset or empty", name)
					}
					return "", fmt.Errorf("config: %s", rest)
				}
				return val, nil
			case ":+":
				if val != "" {
					return rest, nil
				}
				return "", nil
			}
		}
	}
	return os.Getenv(expr), nil
}
