package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zizadhrmaa/DB-Platform/internal/authz"
	"github.com/zizadhrmaa/DB-Platform/internal/dbmanager"
	"github.com/zizadhrmaa/DB-Platform/internal/domain"
)

type Analysis struct {
	Analyze    bool
	Statements []StatementPlan
}

type StatementPlan struct {
	SQL         string
	Explainable bool // false untuk DDL seperti CREATE TABLE atau ALTER TABLE
	Error       string
	Nodes       []PlanNode
	PlanningMs  float64
	ExecutionMs float64 // hanya terisi dengan ANALYZE
}

// PlanNode adalah satu node execution plan yang sudah diratakan dari pohon.
type PlanNode struct {
	Depth        int
	Prefix       string // misalnya "   -> "
	Operation    string
	Target       string
	Condition    string
	StartupCost  float64
	TotalCost    float64
	PlanRows     float64
	HasActual    bool
	ActualMs     float64
	ActualRows   float64
	Loops        float64
	RowsRemoved  float64
	UsesIndex    bool
	IsSeqScan    bool
	JoinStrategy string
}

// Statement lain (DDL) tidak punya plan, tapi tetap dijalankan supaya
// statement berikutnya melihat perubahannya.
var explainable = map[string]bool{
	"SELECT": true, "WITH": true, "INSERT": true, "UPDATE": true,
	"DELETE": true, "MERGE": true, "VALUES": true, "TABLE": true,
}

func isExplainable(stmt string) bool {
	fields := strings.Fields(stripCommentsAndStrings(stmt))
	if len(fields) == 0 {
		return false
	}
	word := strings.ToUpper(strings.TrimLeft(fields[0], "("))
	return explainable[word]
}

// Explain menjalankan semua statement di satu transaction yang selalu
// di-rollback. Nilai sequence yang terpakai saat ANALYZE tidak ikut kembali.
func (s *Store) Explain(ctx context.Context, u *domain.User, dbID, id int64, analyze bool) (*Analysis, error) {
	if u == nil {
		return nil, domain.ErrUnauthenticated
	}
	if s.Authz == nil || s.Targets == nil {
		return nil, fmt.Errorf("explain dependencies not initialized")
	}
	// ANALYZE menjalankan SQL di database target, jadi syaratnya sama
	// dengan menjalankan migration.
	if e := s.Authz.Authorize(ctx, u, dbID, authz.ActionExecuteMigration); e != nil {
		return nil, e
	}
	m, e := s.Get(ctx, dbID, id)
	if e != nil {
		return nil, e
	}
	if !m.Status.Executable() {
		return nil, fmt.Errorf("%w: migration berstatus %s tidak perlu dianalisis lagi", domain.ErrConflict, m.Status)
	}
	// SQL sudah divalidasi saat disimpan, tapi dicek lagi supaya COMMIT
	// tidak mungkin ikut jalan di dalam simulasi.
	if e := ValidateSQL(m.SQL); e != nil {
		return nil, e
	}
	target, e := s.Targets.Conn(ctx, dbID)
	if e != nil {
		return nil, e
	}

	work, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tx, e := target.BeginTx(work, nil)
	if e != nil {
		return nil, dbmanager.SanitizeError(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(work, `SET LOCAL lock_timeout='5s'`); e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(work, `SET LOCAL statement_timeout='30s'`); e != nil {
		return nil, e
	}
	// Kunci yang sama dengan Execute, supaya tidak bentrok dengan eksekusi.
	locked, e := tryLockTarget(work, tx, dbID)
	if e != nil {
		return nil, e
	}
	if !locked {
		return nil, fmt.Errorf("%w: ada migration yang sedang dijalankan di database ini", domain.ErrConflict)
	}

	// SUMMARY membuat waktu planning tetap dilaporkan walaupun tanpa ANALYZE.
	options := "SUMMARY, FORMAT JSON"
	if analyze {
		options = "ANALYZE, " + options
	}
	a := &Analysis{Analyze: analyze}
	for _, stmt := range splitStatements(m.SQL) {
		p := StatementPlan{SQL: stmt, Explainable: isExplainable(stmt)}
		if p.Explainable {
			var raw []byte
			e = tx.QueryRowContext(work, "EXPLAIN ("+options+") "+stmt).Scan(&raw)
			if e == nil {
				e = p.parse(raw)
			}
		} else {
			e = runStatement(work, tx, stmt)
		}
		if e != nil {
			// Transaction sudah batal, statement berikutnya tidak bisa jalan.
			p.Error = errorMessage(e)
			a.Statements = append(a.Statements, p)
			break
		}
		a.Statements = append(a.Statements, p)
	}
	return a, nil
}

// QueryContext memakai extended protocol, jadi teks berisi lebih dari satu
// statement ditolak dan COMMIT tersembunyi tidak bisa ikut jalan.
func runStatement(ctx context.Context, tx *sql.Tx, stmt string) error {
	rows, e := tx.QueryContext(ctx, stmt)
	if e != nil {
		return e
	}
	return rows.Close()
}

type planJSON struct {
	NodeType            string     `json:"Node Type"`
	Operation           string     `json:"Operation"`
	JoinType            string     `json:"Join Type"`
	Strategy            string     `json:"Strategy"`
	Schema              string     `json:"Schema"`
	RelationName        string     `json:"Relation Name"`
	Alias               string     `json:"Alias"`
	IndexName           string     `json:"Index Name"`
	IndexCond           string     `json:"Index Cond"`
	RecheckCond         string     `json:"Recheck Cond"`
	HashCond            string     `json:"Hash Cond"`
	MergeCond           string     `json:"Merge Cond"`
	JoinFilter          string     `json:"Join Filter"`
	Filter              string     `json:"Filter"`
	StartupCost         float64    `json:"Startup Cost"`
	TotalCost           float64    `json:"Total Cost"`
	PlanRows            float64    `json:"Plan Rows"`
	ActualTotalTime     *float64   `json:"Actual Total Time"`
	ActualRows          float64    `json:"Actual Rows"`
	ActualLoops         float64    `json:"Actual Loops"`
	RowsRemovedByFilter float64    `json:"Rows Removed by Filter"`
	Plans               []planJSON `json:"Plans"`
}

func (p *StatementPlan) parse(raw []byte) error {
	var out []struct {
		Plan          planJSON `json:"Plan"`
		PlanningTime  float64  `json:"Planning Time"`
		ExecutionTime float64  `json:"Execution Time"`
	}
	if e := json.Unmarshal(raw, &out); e != nil {
		return fmt.Errorf("hasil EXPLAIN tidak bisa dibaca: %w", e)
	}
	if len(out) == 0 {
		return fmt.Errorf("hasil EXPLAIN kosong")
	}
	p.PlanningMs, p.ExecutionMs = out[0].PlanningTime, out[0].ExecutionTime
	p.Nodes = flatten(out[0].Plan, 0, nil)
	return nil
}

func flatten(n planJSON, depth int, out []PlanNode) []PlanNode {
	node := PlanNode{
		Depth:       depth,
		Prefix:      treePrefix(depth),
		Operation:   operationLabel(n),
		Target:      targetLabel(n),
		Condition:   firstNonEmpty(n.IndexCond, n.HashCond, n.MergeCond, n.JoinFilter, n.RecheckCond, n.Filter),
		StartupCost: n.StartupCost,
		TotalCost:   n.TotalCost,
		PlanRows:    n.PlanRows,
		RowsRemoved: n.RowsRemovedByFilter,
		UsesIndex:   n.IndexName != "",
		IsSeqScan:   n.NodeType == "Seq Scan",
	}
	if strings.Contains(n.NodeType, "Join") || n.NodeType == "Nested Loop" {
		node.JoinStrategy = n.NodeType
	}
	if n.ActualTotalTime != nil {
		node.HasActual = true
		node.ActualMs = *n.ActualTotalTime
		node.ActualRows = n.ActualRows
		node.Loops = n.ActualLoops
	}
	out = append(out, node)
	for _, child := range n.Plans {
		out = flatten(child, depth+1, out)
	}
	return out
}

// Indentasi dibuat sebagai teks karena CSP melarang style inline.
func treePrefix(depth int) string {
	if depth == 0 {
		return ""
	}
	return strings.Repeat("   ", depth-1) + "-> "
}

func operationLabel(n planJSON) string {
	label := n.NodeType
	switch {
	case n.NodeType == "ModifyTable" && n.Operation != "":
		label = n.Operation
	case n.JoinType != "" && n.JoinType != "Inner":
		label += " (" + n.JoinType + ")"
	case n.NodeType == "Aggregate" && n.Strategy != "":
		label += " (" + n.Strategy + ")"
	}
	return label
}

func targetLabel(n planJSON) string {
	rel := n.RelationName
	if rel != "" && n.Schema != "" {
		rel = n.Schema + "." + rel
	}
	if rel != "" && n.Alias != "" && n.Alias != n.RelationName {
		rel += " " + n.Alias
	}
	switch {
	case rel != "" && n.IndexName != "":
		return rel + " memakai index " + n.IndexName
	case n.IndexName != "":
		return "index " + n.IndexName
	}
	return rel
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
