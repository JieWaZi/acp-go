package nativeacp

import (
	"errors"
	"strings"
)

// batchCommandLine keeps command-processor syntax separate from argv quoting.
// Like established stdio launchers, npm local shims require a second caret pass
// because their forwarding command reparses the arguments. Newlines cannot be
// represented safely as a batch argument and are rejected rather than rewritten.
func batchCommandLine(executable string, args []string) (string, error) {
	words := make([]string, 0, len(args)+1)
	if strings.ContainsAny(executable, "\r\n\x00") {
		return "", errors.New("batch executable contains a control delimiter")
	}
	words = append(words, escapeBatchSyntax(executable))
	path := strings.ToLower(strings.ReplaceAll(executable, "\\", "/"))
	shim := strings.Contains(path, "/node_modules/.bin/") && strings.HasSuffix(path, ".cmd")
	for _, arg := range args {
		if strings.ContainsAny(arg, "\r\n\x00") {
			return "", errors.New("batch argument contains a control delimiter")
		}
		var quoted strings.Builder
		quoted.WriteByte('"')
		slashes := 0
		for _, c := range arg {
			if c == '\\' {
				slashes++
				continue
			}
			if c == '"' {
				quoted.WriteString(strings.Repeat("\\", 2*slashes+1))
			} else {
				quoted.WriteString(strings.Repeat("\\", slashes))
			}
			slashes = 0
			quoted.WriteRune(c)
		}
		quoted.WriteString(strings.Repeat("\\", 2*slashes))
		quoted.WriteByte('"')
		word := escapeBatchSyntax(quoted.String())
		if shim {
			word = escapeBatchSyntax(word)
		}
		words = append(words, word)
	}
	return `"` + strings.Join(words, " ") + `"`, nil
}

func escapeBatchSyntax(value string) string {
	var escaped strings.Builder
	for _, c := range value {
		if strings.ContainsRune("()[]%!^\"`<>&|;, *?", c) {
			escaped.WriteByte('^')
		}
		escaped.WriteRune(c)
	}
	return escaped.String()
}
