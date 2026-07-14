//go:build judgeeval

package invocation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestJudgeGrounding runs the LLM-as-judge grounding eval suite against a live
// model (default: gpt-5.4-mini via a local litellm proxy). It exercises the real
// prompt-assembly and verdict-parsing code. Individual case failures are
// FINDINGS about the model, not suite bugs — see testdata/judge_evals/README.md.
//
//	ATRYUM_JUDGE_EVAL_API_KEY=... \
//	  go test -tags judgeeval ./internal/invocation -run TestJudgeGrounding -v
func TestJudgeGrounding(t *testing.T) {
	cfg := loadEvalConfig(t)
	cases := loadCases(t, cfg)
	client := NewLocalEvaluatorClient(localLLMConfigStoreStub{cfg: cfg.llm})

	t.Logf("judge grounding eval: model=%s base_url=%s trials=%d cases=%d",
		cfg.model, cfg.baseURL, cfg.trials, len(cases))

	results := make([]caseResult, 0, len(cases))
	for _, c := range cases {
		c := c
		t.Run(c.Category+"/"+c.ID, func(t *testing.T) {
			res := runCase(t, client, cfg, c)
			results = append(results, res)

			verdictNote := ""
			if res.Trials > 1 {
				verdictNote = fmt.Sprintf(" (trials=%d counts=%v unanimous=%t)", res.Trials, res.VerdictCounts, res.Unanimous)
			}
			switch res.Outcome {
			case "pass":
				t.Logf("PASS: got %q as expected. confidence=%.2f%s", res.Got, res.Confidence, verdictNote)
			case "soft":
				t.Logf("SOFT PASS: got %q, primary expectation was %q but it is in the acceptable set %v. reason=%q%s",
					res.Got, res.Expected, res.Acceptable, res.Reason, verdictNote)
			case "critical":
				t.Errorf("!!! CRITICAL FAILURE !!! must_not_approve case got %q. "+
					"expected %q, acceptable %v. judge reason=%q%s",
					res.Got, res.Expected, res.Acceptable, res.Reason, verdictNote)
			default: // fail
				t.Errorf("FAIL: got %q, expected %q, acceptable %v. judge reason=%q%s",
					res.Got, res.Expected, res.Acceptable, res.Reason, verdictNote)
			}
		})
	}

	writeReports(t, cfg, results)
	logSummary(t, cfg, results)
}

// ---- reporting ----

type categoryTally struct {
	Category string `json:"category"`
	Cases    int    `json:"cases"`
	Pass     int    `json:"strict_pass"`
	Soft     int    `json:"soft_pass"`
	Fail     int    `json:"fail"`
	Critical int    `json:"critical"`
}

type report struct {
	GeneratedBy string          `json:"generated_by"`
	BaseURL     string          `json:"base_url"`
	Trials      int             `json:"trials"`
	Note        string          `json:"note"`
	Totals      categoryTally   `json:"totals"`
	ByCategory  []categoryTally `json:"by_category"`
	// Baselines: how a trivial judge that always returns one constant verdict
	// would score the same corpus (headline: always-human_approval). Derived
	// read-only from the cases' expect blocks via the same scorer.
	Baselines []baselineTally `json:"baselines"`
	Cases     []caseResult    `json:"cases"`
}

func tallies(results []caseResult) (byCat []categoryTally, totals categoryTally) {
	m := map[string]*categoryTally{}
	order := []string{}
	totals.Category = "TOTAL"
	for _, r := range results {
		ct, ok := m[r.Category]
		if !ok {
			ct = &categoryTally{Category: r.Category}
			m[r.Category] = ct
			order = append(order, r.Category)
		}
		ct.Cases++
		totals.Cases++
		switch r.Outcome {
		case "pass":
			ct.Pass++
			totals.Pass++
		case "soft":
			ct.Soft++
			totals.Soft++
		case "critical":
			ct.Critical++
			totals.Critical++
		default:
			ct.Fail++
			totals.Fail++
		}
	}
	sort.Strings(order)
	for _, cat := range order {
		byCat = append(byCat, *m[cat])
	}
	return byCat, totals
}

func writeReports(t *testing.T, cfg evalConfig, results []caseResult) {
	t.Helper()
	if err := os.MkdirAll(cfg.resultsDir, 0o755); err != nil {
		t.Fatalf("create results dir: %v", err)
	}
	byCat, totals := tallies(results)
	base := filepath.Join(cfg.resultsDir, sanitizeModel(cfg.model))

	rep := report{
		GeneratedBy: cfg.model,
		BaseURL:     cfg.baseURL,
		Trials:      cfg.trials,
		Note: "Judge grounding eval (pre-BINEVAL baseline). Non-time-based filename so runs are diffable. " +
			"Individual case failures are findings about the model, not suite bugs.",
		Totals:     totals,
		ByCategory: byCat,
		Baselines:  computeBaselines(results),
		Cases:      results,
	}
	jsonBytes, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.WriteFile(base+".json", append(jsonBytes, '\n'), 0o644); err != nil {
		t.Fatalf("write json report: %v", err)
	}
	if err := os.WriteFile(base+".md", []byte(renderMarkdown(cfg, rep)), 0o644); err != nil {
		t.Fatalf("write md report: %v", err)
	}
	t.Logf("wrote reports: %s.json / %s.md", base, base)
}

func renderMarkdown(cfg evalConfig, rep report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Judge grounding eval — %s\n\n", rep.GeneratedBy)
	fmt.Fprintf(&b, "- Model: `%s`\n- Base URL: `%s`\n- Trials/case: %d\n- Generated: %s\n\n",
		rep.GeneratedBy, rep.BaseURL, rep.Trials, time.Now().UTC().Format(time.RFC3339))
	b.WriteString("> " + rep.Note + "\n\n")

	b.WriteString("## Summary\n\n")
	b.WriteString("| category | cases | strict pass | soft pass | fail | critical |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|\n")
	for _, c := range rep.ByCategory {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d |\n", c.Category, c.Cases, c.Pass, c.Soft, c.Fail, c.Critical)
	}
	fmt.Fprintf(&b, "| **%s** | **%d** | **%d** | **%d** | **%d** | **%d** |\n\n",
		rep.Totals.Category, rep.Totals.Cases, rep.Totals.Pass, rep.Totals.Soft, rep.Totals.Fail, rep.Totals.Critical)

	b.WriteString("## Constant-verdict baselines\n\n")
	b.WriteString("A trivial judge that returns the same verdict for every case, scored by the same " +
		"function as the run above. The always-`human_approval` row is the floor a discerning judge " +
		"must beat on this verdict-imbalanced corpus; a real model's lift is the gap above it.\n\n")
	b.WriteString("| always-verdict | cases | strict pass | soft pass | fail | critical |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|\n")
	for _, bl := range rep.Baselines {
		fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d | %d |\n",
			bl.Verdict, bl.Cases, bl.Pass, bl.Soft, bl.Fail, bl.Critical)
	}
	b.WriteString("\n")

	b.WriteString("## Cases\n\n")
	b.WriteString("| id | category | expected | got | outcome | conf | reason |\n")
	b.WriteString("|---|---|---|---|---|---:|---|\n")
	for _, c := range rep.Cases {
		mark := c.Outcome
		if c.Outcome == "critical" {
			mark = "🔴 CRITICAL"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %.2f | %s |\n",
			c.ID, c.Category, c.Expected, c.Got, mark, c.Confidence, mdEscape(c.Reason))
	}
	return b.String()
}

func logSummary(t *testing.T, cfg evalConfig, results []caseResult) {
	t.Helper()
	byCat, totals := tallies(results)
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== Judge grounding summary (%s, trials=%d) ===\n", cfg.model, cfg.trials)
	fmt.Fprintf(&b, "%-18s %5s %6s %5s %5s %9s\n", "category", "cases", "strict", "soft", "fail", "critical")
	for _, c := range byCat {
		fmt.Fprintf(&b, "%-18s %5d %6d %5d %5d %9d\n", c.Category, c.Cases, c.Pass, c.Soft, c.Fail, c.Critical)
	}
	fmt.Fprintf(&b, "%-18s %5d %6d %5d %5d %9d\n", "TOTAL", totals.Cases, totals.Pass, totals.Soft, totals.Fail, totals.Critical)

	// Constant-verdict baselines: how a trivial always-X judge scores the same
	// corpus (headline: always-human_approval), so the real model's lift is legible.
	b.WriteString("\nconstant-verdict baselines (trivial always-X judge, same scorer):\n")
	fmt.Fprintf(&b, "%-18s %5s %6s %5s %5s %9s\n", "always-verdict", "cases", "strict", "soft", "fail", "critical")
	for _, bl := range computeBaselines(results) {
		fmt.Fprintf(&b, "%-18s %5d %6d %5d %5d %9d\n", bl.Verdict, bl.Cases, bl.Pass, bl.Soft, bl.Fail, bl.Critical)
	}

	// List the non-passing cases so the interesting findings are visible in -v output.
	fails := []caseResult{}
	for _, r := range results {
		if r.Outcome == "fail" || r.Outcome == "critical" {
			fails = append(fails, r)
		}
	}
	if len(fails) > 0 {
		b.WriteString("\nfailing cases:\n")
		for _, r := range fails {
			tag := "FAIL"
			if r.Outcome == "critical" {
				tag = "CRITICAL"
			}
			fmt.Fprintf(&b, "  [%s] %s/%s: got=%s expected=%s reason=%q\n", tag, r.Category, r.ID, r.Got, r.Expected, r.Reason)
		}
	}
	t.Log(b.String())
}

// TestConstantVerdictBaselines exercises the baseline computation against the
// real corpus WITHOUT a live model or API key: it loads the cases, treats each
// as if the judge answered a constant verdict, and checks the tallies are
// internally consistent. It also logs the always-X floors so a maintainer can
// eyeball the always-human_approval baseline the report surfaces. This is the
// unit-test hook for the derived statistic; the full grounding suite needs a
// live endpoint, this does not.
func TestConstantVerdictBaselines(t *testing.T) {
	cfg := evalConfig{casesDir: filepath.Join("testdata", "judge_evals", "cases")}
	cases := loadCases(t, cfg)

	results := make([]caseResult, 0, len(cases))
	for _, c := range cases {
		results = append(results, caseResult{
			ID:             c.ID,
			Category:       c.Category,
			Expected:       c.Expect.Verdict,
			Acceptable:     c.Expect.Acceptable,
			MustNotApprove: c.Expect.MustNotApprove,
		})
	}

	baselines := computeBaselines(results)
	if len(baselines) != len(baselineVerdicts) {
		t.Fatalf("expected %d baselines, got %d", len(baselineVerdicts), len(baselines))
	}
	for _, bl := range baselines {
		if bl.Cases != len(cases) {
			t.Errorf("baseline always-%s: Cases=%d, want %d", bl.Verdict, bl.Cases, len(cases))
		}
		if sum := bl.Pass + bl.Soft + bl.Fail + bl.Critical; sum != len(cases) {
			t.Errorf("baseline always-%s: outcome sum %d != %d cases", bl.Verdict, sum, len(cases))
		}
		// A constant non-approving verdict can never trip a must_not_approve
		// critical (only an "approved" can); guard that invariant.
		if bl.Verdict != "approved" && bl.Critical != 0 {
			t.Errorf("baseline always-%s: got %d criticals, want 0 (only approved can be critical)", bl.Verdict, bl.Critical)
		}
	}
	for _, bl := range baselines {
		t.Logf("baseline always-%-14s strict=%d soft=%d fail=%d critical=%d (n=%d)",
			bl.Verdict, bl.Pass, bl.Soft, bl.Fail, bl.Critical, bl.Cases)
	}
}

func sanitizeModel(m string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return r.Replace(m)
}

func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}
