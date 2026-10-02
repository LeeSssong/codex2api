package accountops

import (
	"testing"
	"time"
)

func TestQualityOutcome(t *testing.T) {
	for _, tt := range []struct {
		r    []Sample
		want string
	}{{nil, "inconclusive"}, {[]Sample{{Judgment: Judgment{Verdict: "correct"}}, {Judgment: Judgment{Verdict: "correct"}}}, "passed"}, {[]Sample{{Judgment: Judgment{Verdict: "correct"}}, {Error: "timeout"}}, "inconclusive"}, {[]Sample{{Judgment: Judgment{Verdict: "incorrect"}}, {Error: "timeout"}}, "failed"}} {
		if got := Outcome(tt.r); got != tt.want {
			t.Fatalf("got %s want %s", got, tt.want)
		}
	}
}
func TestJudgmentStrictJSON(t *testing.T) {
	for _, s := range []string{`{"verdict":"correct","reason":"equivalent"}`, `{"verdict":"unknown","reason":"uncertain"}`} {
		if _, e := ParseJudgment(s); e != nil {
			t.Fatal(e)
		}
	}
	for _, s := range []string{`{"verdict":"correct","verdict":"incorrect","reason":"x"}`, `{"verdict":"correct","reason":"x","extra":1}`, `{"verdict":"correct","reason":""}`, `{"verdict":"correct","reason":"x"} {}`} {
		if _, e := ParseJudgment(s); e == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}
func TestPlanValidation(t *testing.T) {
	p := Plan{AccountID: 1, Model: "model", Prompt: "question", Cron: "*/5 * * * *", Samples: 8, ExpectedAnswer: "21", Action: "disable_scheduling", Judge: &JudgeConfig{GroupID: 2, ModelID: "judge", Prompt: "grade"}}
	if _, e := p.Next(time.Now()); e != nil {
		t.Fatal(e)
	}
	p.Action = "record_only"
	if _, e := p.Next(time.Now()); e == nil {
		t.Fatal("unsupported action accepted")
	}
	p.Action = "remove_groups"
	if _, e := p.Next(time.Now()); e == nil {
		t.Fatal("empty groups accepted")
	}
}
func TestPlanCronUsesConfiguredLocalTimezone(t *testing.T) {
	old := time.Local
	defer func() { time.Local = old }()
	zone, e := time.LoadLocation("Asia/Shanghai")
	if e != nil {
		t.Fatal(e)
	}
	time.Local = zone
	p := Plan{AccountID: 1, Model: "model", Prompt: "q", ExpectedAnswer: "21", Action: "disable_scheduling", Cron: "0 9 * * *", Samples: 1}
	next, e := p.Next(time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
	if e != nil || next.In(zone).Hour() != 9 || next.In(zone).Day() != 25 {
		t.Fatalf("wrong local schedule %v %v", next, e)
	}
}
