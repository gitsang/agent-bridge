package bridge

import (
	"fmt"
	"strings"
	"unicode"
)

type ParsedInput struct {
	Content    string
	Invocation *Invocation
}

type Invocation struct {
	Tokens      []string
	Positionals []string
	Flags       map[string]string
}

func ParseInput(content string) (*ParsedInput, error) {
	resolvedContent := strings.TrimSpace(content)
	if resolvedContent == "" {
		return nil, fmt.Errorf("message content cannot be empty")
	}

	if !strings.HasPrefix(resolvedContent, "/") {
		return &ParsedInput{Content: resolvedContent}, nil
	}

	tokens, err := tokenizeSlashContent(strings.TrimSpace(strings.TrimPrefix(resolvedContent, "/")))
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("slash command is required")
	}

	invocation, err := buildInvocation(tokens)
	if err != nil {
		return nil, err
	}

	return &ParsedInput{Invocation: invocation}, nil
}

func tokenizeSlashContent(content string) ([]string, error) {
	tokens := make([]string, 0, 8)
	builder := strings.Builder{}

	var quote rune
	escaped := false

	for _, current := range content {
		if escaped {
			builder.WriteRune(current)
			escaped = false
			continue
		}

		if quote != 0 {
			if current == quote {
				quote = 0
				continue
			}
			if current == '\\' && quote == '"' {
				escaped = true
				continue
			}
			builder.WriteRune(current)
			continue
		}

		switch {
		case current == '\\':
			escaped = true
		case current == '\'' || current == '"':
			quote = current
		case unicode.IsSpace(current):
			if builder.Len() == 0 {
				continue
			}
			tokens = append(tokens, builder.String())
			builder.Reset()
		default:
			builder.WriteRune(current)
		}
	}

	if escaped {
		return nil, fmt.Errorf("invalid trailing escape in command")
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in command")
	}
	if builder.Len() > 0 {
		tokens = append(tokens, builder.String())
	}

	return tokens, nil
}

func buildInvocation(tokens []string) (*Invocation, error) {
	flags := map[string]string{}
	positionals := make([]string, 0, len(tokens))

	for index := 0; index < len(tokens); index++ {
		current := strings.TrimSpace(tokens[index])
		if current == "" {
			continue
		}

		if current == "--" {
			positionals = append(positionals, tokens[index+1:]...)
			break
		}

		if !strings.HasPrefix(current, "--") {
			positionals = append(positionals, current)
			continue
		}

		flag := strings.TrimSpace(strings.TrimPrefix(current, "--"))
		if flag == "" {
			return nil, fmt.Errorf("invalid flag syntax")
		}

		if strings.Contains(flag, "=") {
			pair := strings.SplitN(flag, "=", 2)
			name := strings.TrimSpace(strings.ToLower(pair[0]))
			value := strings.TrimSpace(pair[1])
			if name == "" {
				return nil, fmt.Errorf("invalid flag syntax: %s", current)
			}
			flags[name] = value
			continue
		}

		name := strings.TrimSpace(strings.ToLower(flag))
		if name == "" {
			return nil, fmt.Errorf("invalid flag syntax: %s", current)
		}

		value := "true"
		if index+1 < len(tokens) && !strings.HasPrefix(strings.TrimSpace(tokens[index+1]), "--") {
			value = strings.TrimSpace(tokens[index+1])
			index++
		}
		flags[name] = value
	}

	return &Invocation{Tokens: tokens, Positionals: positionals, Flags: flags}, nil
}
