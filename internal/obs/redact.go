package obs

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Kata kunci atribut sensitif yang wajib diredaksi.
var sensitiveKeywords = []string{
	"password",
	"passwd",
	"pwd",
	"secret",
	"token",
	"session",
	"cookie",
	"csrf",
	"dsn",
	"authorization",
	"ciphertext",
	"api_key",
	"apikey",
	"private_key",
}

const RedactedPlaceholder = "[REDACTED]"

// IsSensitiveKey memeriksa apakah nama key mengandung salah satu kata kunci sensitif.
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	k = strings.ReplaceAll(k, "_", "")
	k = strings.ReplaceAll(k, "-", "")
	for _, word := range sensitiveKeywords {
		w := strings.ReplaceAll(word, "_", "")
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

var (
	// Regex mendeteksi format DSN postgres/mysql dan redaksi passwordnya.
	urlPasswordRegex = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^:]+:)([^@]+)(@)`)
	mysqlDSNRegex    = regexp.MustCompile(`^([^:]+):([^@]+)@tcp\(`)
	bearerTokenRegex = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/-]+=*`)
)

// RedactString meredaksi string jika terdapat credential seperti password URL/DSN atau bearer token.
func RedactString(s string) string {
	if urlPasswordRegex.MatchString(s) {
		s = urlPasswordRegex.ReplaceAllString(s, "${1}"+RedactedPlaceholder+"${3}")
	}
	if mysqlDSNRegex.MatchString(s) {
		s = mysqlDSNRegex.ReplaceAllString(s, "${1}:"+RedactedPlaceholder+"@tcp(")
	}
	if bearerTokenRegex.MatchString(s) {
		s = bearerTokenRegex.ReplaceAllString(s, "${1}"+RedactedPlaceholder)
	}
	return s
}

// RedactValue meredaksi nilai berdasarkan nama key dan isi nilainya.
func RedactValue(key string, val any) any {
	if IsSensitiveKey(key) {
		return RedactedPlaceholder
	}
	switch v := val.(type) {
	case string:
		return RedactString(v)
	case fmt.Stringer:
		return RedactString(v.String())
	case []any:
		res := make([]any, len(v))
		for i, item := range v {
			res[i] = RedactValue(fmt.Sprintf("%s_%d", key, i), item)
		}
		return res
	case map[string]any:
		res := make(map[string]any, len(v))
		for mk, mv := range v {
			res[mk] = RedactValue(mk, mv)
		}
		return res
	case *url.URL:
		if v == nil {
			return nil
		}
		redacted := *v
		if _, has := redacted.User.Password(); has {
			redacted.User = url.UserPassword(redacted.User.Username(), RedactedPlaceholder)
		}
		return redacted.String()
	default:
		return val
	}
}

// RedactArgs meredaksi slice argumen SQL atau parameter query.
func RedactArgs(args []any) []any {
	if len(args) == 0 {
		return nil
	}
	res := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case string:
			res[i] = RedactString(v)
		case []byte:
			res[i] = fmt.Sprintf("<%d bytes>", len(v))
		default:
			res[i] = a
		}
	}
	return res
}
