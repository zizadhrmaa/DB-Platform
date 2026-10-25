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
// dan dollar-quoted string dengan spasi, termasuk titik koma di dalamnya.
// Panjang hasilnya sama dengan aslinya, jadi posisi titik koma bisa dipakai
// untuk memotong teks asli per statement.
func stripCommentsAndStrings(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c, start := s[i], i
		switch {
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
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
		case c == '\'' || c == '"':
			q := c
			escapes := q == '\'' && isEString(s, start)
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
				if escapes && s[i] == '\\' && i+1 < len(s) { // E'..\'..'
					i += 2
					continue
				}
				i++
			}
		case c == '$':
			tag, ok := dollarTag(s[i:])
			if !ok {
				b.WriteByte(c)
				i++
				continue
			}
			end := strings.Index(s[i+len(tag):], tag)
			if end < 0 {
				i = len(s)
			} else {
				i += len(tag) + end + len(tag)
			}
		default:
			b.WriteByte(c)
			i++
			continue
		}
		b.WriteString(strings.Repeat(" ", min(i, len(s))-start))
	}
	return b.String()
}

// isEString mengecek apakah tanda kutip di posisi i adalah awal string
// E'...'. Hanya di string seperti ini backslash berfungsi sebagai escape;
// di string biasa backslash adalah karakter biasa.
func isEString(s string, i int) bool {
	if i == 0 || (s[i-1] != 'E' && s[i-1] != 'e') {
		return false
	}
	return i == 1 || !isIdentChar(s[i-2])
}

func isIdentChar(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// splitStatements memecah SQL per statement. Titik koma di dalam string,
// komentar, atau body fungsi tidak dianggap pemisah. Statement yang isinya
// hanya komentar dibuang.
func splitStatements(sql string) []string {
	masked := stripCommentsAndStrings(sql)
	var out []string
	start := 0
	for i := 0; i <= len(masked); i++ {
		if i < len(masked) && masked[i] != ';' {
			continue
		}
		if strings.TrimSpace(masked[start:i]) != "" {
			out = append(out, strings.TrimSpace(sql[start:i]))
		}
		start = i + 1
	}
	return out
}

var dollarTagRe = regexp.MustCompile(`^\$[A-Za-z_][A-Za-z0-9_]*\$|^\$\$`)

func dollarTag(s string) (string, bool) {
	t := dollarTagRe.FindString(s)
	return t, t != ""
}
