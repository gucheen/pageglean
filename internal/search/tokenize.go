package search

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

func Tokens(value string) []string {
	value = strings.ToLower(norm.NFKC.String(value))
	var tokens []string
	var run []rune
	var runKind int
	flush := func() {
		if len(run) == 0 {
			return
		}
		switch runKind {
		case 1:
			if len(run) == 1 {
				tokens = append(tokens, string(run))
			} else {
				for index := 0; index < len(run)-1; index++ {
					tokens = append(tokens, string(run[index:index+2]))
				}
			}
		case 2:
			tokens = append(tokens, string(run))
		}
		run = run[:0]
	}
	for _, char := range []rune(value) {
		kind := 0
		if unicode.Is(unicode.Han, char) {
			kind = 1
		} else if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' || char == '-' {
			kind = 2
		}
		if kind == 0 {
			flush()
			runKind = 0
			continue
		}
		if runKind != 0 && runKind != kind {
			flush()
		}
		runKind = kind
		run = append(run, char)
	}
	flush()
	return deduplicate(tokens)
}

func Document(value string) string { return strings.Join(Tokens(value), " ") }

func Query(value string) string {
	tokens := Tokens(value)
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		part := `"` + strings.ReplaceAll(token, `"`, `""`) + `"`
		if isPrefixToken(token) {
			part += `*`
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " AND ")
}

func isPrefixToken(token string) bool {
	for _, char := range token {
		if unicode.Is(unicode.Han, char) {
			return false
		}
	}
	return token != ""
}

func SnippetNeedle(value string) string {
	normalized := strings.TrimSpace(norm.NFKC.String(value))
	if normalized != "" {
		return normalized
	}
	tokens := Tokens(value)
	if len(tokens) > 0 {
		return tokens[0]
	}
	return ""
}

func deduplicate(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
