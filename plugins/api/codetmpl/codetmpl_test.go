package codetmpl

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type corpusCase struct {
	Name           string          `json:"name"`
	Template       string          `json:"template"`
	Repeat         *corpusRepeat   `json:"repeat"`
	Escape         string          `json:"escape"`
	OutputMaxBytes int             `json:"outputMaxBytes"`
	Model          json.RawMessage `json:"model"`
	Output         *string         `json:"output"`
	Error          string          `json:"error"`
}

type corpusRepeat struct {
	Unit  string `json:"unit"`
	Count int    `json:"count"`
}

func contractVocabulary(t *testing.T) (TemplateLimits, Vocabulary, int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "contribution-points.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Points map[string]struct {
			Limits map[string]int      `json:"limits"`
			Lists  map[string][]string `json:"lists"`
		} `json:"points"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	spec := contract.Points["httpCodeGenerators"]
	return TemplateLimits{MaxNodes: spec.Limits["templateNodesMax"], MaxDepth: spec.Limits["sectionDepthMax"]},
		Vocabulary{
			Variables: spec.Lists["variables"], ItemVariables: spec.Lists["itemVariables"],
			ListSections: spec.Lists["listSections"], FlagSections: spec.Lists["flagSections"],
			ItemFlagSections: spec.Lists["itemFlagSections"], Filters: spec.Lists["filters"],
		}, spec.Limits["outputMaxBytes"]
}

func TestCorpus(t *testing.T) {
	limits, vocab, outputMax := contractVocabulary(t)
	if limits.MaxNodes == 0 || limits.MaxDepth == 0 || len(vocab.Filters) == 0 {
		t.Fatal("contract vocabulary is missing")
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "tools", "verify", "fixtures", "codetmpl", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpus files: %v", err)
	}
	total := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var corpus struct {
			Cases []corpusCase `json:"cases"`
		}
		if err := json.Unmarshal(raw, &corpus); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, item := range corpus.Cases {
			total++
			t.Run(item.Name, func(t *testing.T) {
				src := item.Template
				if item.Repeat != nil {
					src = strings.Repeat(item.Repeat.Unit, item.Repeat.Count)
				}
				tmpl, err := Compile(src, limits, vocab)
				if err == nil {
					if parseErr := Parse(src, limits, vocab); parseErr != nil {
						t.Fatalf("Parse disagrees with Compile: %v", parseErr)
					}
					var model CodeModel
					if len(item.Model) > 0 {
						if err := json.Unmarshal(item.Model, &model); err != nil {
							t.Fatal(err)
						}
					}
					max := outputMax
					if item.OutputMaxBytes > 0 {
						max = item.OutputMaxBytes
					}
					var out string
					out, err = tmpl.Render(model, item.Escape, max)
					if err == nil {
						if item.Output == nil {
							t.Fatalf("rendered %q, want error %s", out, item.Error)
						}
						if out != *item.Output {
							t.Fatalf("output\n got %q\nwant %q", out, *item.Output)
						}
						return
					}
				}
				var templateErr *TemplateError
				if !errors.As(err, &templateErr) {
					t.Fatalf("error %v is not a TemplateError", err)
				}
				if item.Error == "" || templateErr.Code != item.Error {
					t.Fatalf("error %v, want code %q", err, item.Error)
				}
			})
		}
	}
	if total < 30 {
		t.Fatalf("corpus has only %d cases", total)
	}
}

func TestRenderRejectsUnknownEscape(t *testing.T) {
	limits, vocab, _ := contractVocabulary(t)
	tmpl, err := Compile("{{url}}", limits, vocab)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmpl.Render(CodeModel{}, "eval", 0); err == nil {
		t.Fatal("unknown escape rendered")
	}
}

func TestErrorsCarryLines(t *testing.T) {
	limits, vocab, _ := contractVocabulary(t)
	err := Parse("a\nb\n{{nope}}", limits, vocab)
	var templateErr *TemplateError
	if !errors.As(err, &templateErr) || templateErr.Line != 3 || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("got %v", err)
	}
}
