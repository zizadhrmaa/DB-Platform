package migration

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

const MaxSQLBytes = 512 << 10 // 512 KiB

// ValidateSQL menolak perintah kontrol transaksi dan perintah yang tidak
// bisa dijalankan PostgreSQL di dalam transaction block. Komentar dan
// string dibuang dulu supaya 'BEGIN' di dalam data tidak ikut terdeteksi.

// Ini bukan pengaman: user edit memang boleh menjalankan SQL apa saja yang
// diizinkan role database target. Tujuannya cuma memberi pesan error
// yang lebih jelas.
func ValidateSQL(sql string) error {
	if len(sql) > MaxSQLBytes {
		return fmt.Errorf("%w: SQL melebihi %d KiB", domain.ErrInvalidInput, MaxSQLBytes>>10)
	}
	cleaned := stripCommentsAndStrings(sql)
	if strings.TrimSpace(strings.ReplaceAll(cleaned, ";", "")) == "" {
		return fmt.Errorf("%w: SQL kosong", domain.ErrInvalidInput)
	}
	for _, stmt := range strings.Split(cleaned, ";") {
		s := strings.ToUpper(strings.Join(strings.Fields(stmt), " "))
		if s == "" {
			continue
		}
		for _, rule := range forbidden {
			if rule.re.MatchString(s) {
				return fmt.Errorf("%w: %s", domain.ErrInvalidInput, rule.reason)
			}
		}
	}
	return nil
}

var forbidden = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`^(BEGIN|COMMIT|ROLLBACK|END|ABORT|START TRANSACTION)\b`),
		"jangan menulis BEGIN/COMMIT/ROLLBACK; platform sudah membungkus migration dalam transaction"},
	{regexp.MustCompile(`^(CREATE|DROP) (UNIQUE )?INDEX CONCURRENTLY\b`),
		"INDEX CONCURRENTLY tidak bisa dijalankan di dalam transaction"},
	{regexp.MustCompile(`^REINDEX\b.*\bCONCURRENTLY\b`), "REINDEX CONCURRENTLY tidak bisa dijalankan di dalam transaction"},
	{regexp.MustCompile(`^VACUUM\b`), "VACUUM tidak bisa dijalankan di dalam transaction"},
	{regexp.MustCompile(`^(CREATE|DROP|ALTER) DATABASE\b`), "CREATE/DROP/ALTER DATABASE tidak diizinkan di migration"},
	{regexp.MustCompile(`^(CREATE|DROP) TABLESPACE\b`), "CREATE/DROP TABLESPACE tidak bisa dijalankan di dalam transaction"},
	{regexp.MustCompile(`^ALTER SYSTEM\b`), "ALTER SYSTEM tidak bisa dijalankan di dalam transaction"},
}

// stripCommentsAndStrings mengganti komentar, string, quoted identifier,
// dan dollar-quoted string dengan spasi, termasuk titik koma di dalamnya,
// supaya split per statement aman.
func stripCommentsAndStrings(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			depth := 0 // komentar blok di PostgreSQL bisa bersarang
			for i < len(s) {
				if s[i] == '/' && i+1 < len(s) && s[i+1] == '*' {
					depth++
					i += 2
				} else if s[i] == '*' && i+1 < len(s) && s[i+1] == '/' {
					depth--
					i += 2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}
			b.WriteByte(' ')
		case c == '\'' || c == '"':
			q := c
			i++
			for i < len(s) {
				if s[i] == q {
					if i+1 < len(s) && s[i+1] == q { // '' atau ""
						i += 2
						continue
					}
					i++
					break
				}
				if q == '\'' && s[i] == '\\' && i+1 < len(s) { // E'..\'..'
					i += 2
					continue
				}
				i++
			}
			b.WriteByte(' ')
		case c == '$':
			if tag, ok := dollarTag(s[i:]); ok {
				end := strings.Index(s[i+len(tag):], tag)
				if end < 0 {
					i = len(s)
				} else {
					i += len(tag) + end + len(tag)
				}
				b.WriteByte(' ')
			} else {
				b.WriteByte(c)
				i++
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

var dollarTagRe = regexp.MustCompile(`^\$[A-Za-z_][A-Za-z0-9_]*\$|^\$\$`)

func dollarTag(s string) (string, bool) {
	t := dollarTagRe.FindString(s)
	return t, t != ""
}
