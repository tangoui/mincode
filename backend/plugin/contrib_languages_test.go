package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const monarchGrammarFixture = `{
  "defaultToken": "",
  "tokenPostfix": ".acme",
  "keywords": ["let", "fn", "if", "else"],
  "digits": "\\d+(_+\\d+)*",
  "brackets": [{"open": "{", "close": "}", "token": "delimiter.curly"}, ["(", ")", "delimiter.parenthesis"]],
  "tokenizer": {
    "root": [
      {"include": "@whitespace"},
      ["[a-z_]\\w*", {"cases": {"@keywords": "keyword", "@default": "identifier"}}],
      ["@digits", "number"],
      ["\"", "string", "@string"]
    ],
    "whitespace": [["[ \\t]+", "white"], ["#.*$", "comment"]],
    "string": [["[^\"\\\\]+", "string"], ["\"", {"token": "string", "next": "@pop"}]]
  }
}`

const testLanguageConfiguration = `{"comments":{"lineComment":"#","blockComment":["/*","*/"]},"brackets":[["{","}"],["(",")"]],"autoClosingPairs":[["{","}"],{"open":"\"","close":"\"","notIn":["string"]}],"surroundingPairs":[["(",")"],{"open":"\"","close":"\""}],"colorizedBracketPairs":[["{","}"]],"autoCloseBefore":";:.,=}])> \n\t","folding":{"markers":{"start":"^\\s*#region","end":"^\\s*#endregion"},"offSide":false},"wordPattern":"[A-Za-z_]\\w*","indentationRules":{"increaseIndentPattern":"\\{\\s*$","decreaseIndentPattern":"^\\s*\\}"}}`

func writeLanguagePackage(t *testing.T, entries []any, files map[string]string) string {
	t.Helper()
	return writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["id"] = "language-test"
		manifest["contributes"] = map[string]any{"languages": entries}
	}, nil, files)
}

func acmeLanguageEntry() map[string]any {
	return map[string]any{
		"id": "acme", "aliases": []string{"Acme", "acme"}, "extensions": []string{".acme"},
		"filenames": []string{"Acmefile"}, "filenamePatterns": []string{"**/*.acme.json"},
		"grammar": "syntaxes/acme.json", "configuration": "languages/acme.configuration.json",
	}
}

func acmeLanguageFiles() map[string]string {
	return map[string]string{"syntaxes/acme.json": monarchGrammarFixture, "languages/acme.configuration.json": testLanguageConfiguration}
}

func TestLanguagesDeclarativePackageStrictAndSnapshot(t *testing.T) {
	useTemporaryPluginData(t)
	root := writeLanguagePackage(t, []any{acmeLanguageEntry(), map[string]any{"id": "ini", "extensions": []string{".acmerc"}}}, acmeLanguageFiles())
	inspection, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := inspection.Manifest.contribution(languagePointKey)
	if resolved == nil || len(resolved.Entries) != 2 || len(resolved.Issues) != 0 {
		t.Fatalf("resolved = %+v", resolved)
	}
	acme := resolved.Entries[0].(LanguageContribution)
	if acme.ID != "acme" || acme.GrammarPath != "syntaxes/acme.json" || acme.ConfigurationPath != "languages/acme.configuration.json" {
		t.Fatalf("acme = %+v", acme)
	}
	// Key order of the grammar file is kept (Monarch evaluates in order).
	grammar := string(acme.Grammar)
	if strings.Index(grammar, `"root"`) > strings.Index(grammar, `"whitespace"`) || strings.Index(grammar, `"@keywords"`) > strings.Index(grammar, `"@default"`) ||
		!strings.HasPrefix(grammar, `{"defaultToken":"","tokenPostfix":".acme"`) || strings.Contains(grammar, "\n") {
		t.Fatalf("grammar = %s", grammar)
	}

	manager := newPluginManager()
	manifest, err := manager.InstallLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled(context.Background(), manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Contributions([]string{languagePointKey})
	if len(snapshot.Plugins) != 1 || len(snapshot.Plugins[0].Points[languagePointKey].Entries) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot.Plugins[0].Points[languagePointKey].Entries[1])
	if err != nil || string(encoded) != `{"id":"ini","aliases":[],"extensions":[".acmerc"],"filenames":[],"filenamePatterns":[]}` {
		t.Fatalf("association-only encoded = %s %v", encoded, err)
	}
	encoded, err = json.Marshal(snapshot.Plugins[0].Points[languagePointKey].Entries[0])
	if err != nil || !strings.Contains(string(encoded), `"grammar":{"defaultToken"`) || !strings.Contains(string(encoded), `"configuration":{"comments"`) ||
		!strings.Contains(string(encoded), `"aliases":["Acme","acme"]`) {
		t.Fatalf("encoded = %s %v", encoded, err)
	}
}

func TestLanguagesEntryRules(t *testing.T) {
	long := strings.Repeat("a", 70)
	cases := map[string]struct {
		entry map[string]any
		want  string
	}{
		"bad id":                  {map[string]any{"id": "Acme", "aliases": []string{"A"}}, "must match"},
		"id with plus":            {map[string]any{"id": "c++x", "aliases": []string{"A"}}, "must match"},
		"unknown key":             {map[string]any{"id": "acme", "aliases": []string{"A"}, "mimetypes": []string{"x"}}, "unknown field"},
		"new id without aliases":  {map[string]any{"id": "acme", "extensions": []string{".acme"}}, "aliases"},
		"blank alias":             {map[string]any{"id": "acme", "aliases": []string{" "}}, "alias"},
		"bidi alias":              {map[string]any{"id": "acme", "aliases": []string{"a‮b"}}, "alias"},
		"duplicate alias":         {map[string]any{"id": "acme", "aliases": []string{"A", "A"}}, "duplicate language alias"},
		"reserved with aliases":   {map[string]any{"id": "json", "aliases": []string{"JSON"}, "extensions": []string{".acmejson"}}, "reserved"},
		"reserved with grammar":   {map[string]any{"id": "python", "grammar": "syntaxes/acme.json", "extensions": []string{".pyx"}}, "reserved"},
		"reserved without assocs": {map[string]any{"id": "json"}, "reserved"},
		"extension without dot":   {map[string]any{"id": "acme", "aliases": []string{"A"}, "extensions": []string{"acme"}}, "extension"},
		"extension wildcard":      {map[string]any{"id": "acme", "aliases": []string{"A"}, "extensions": []string{".*"}}, "extension"},
		"duplicate extension":     {map[string]any{"id": "acme", "aliases": []string{"A"}, "extensions": []string{".acme", ".ACME"}}, "duplicate language extension"},
		"filename with slash":     {map[string]any{"id": "acme", "aliases": []string{"A"}, "filenames": []string{"a/b"}}, "plain file name"},
		"filename dotdot":         {map[string]any{"id": "acme", "aliases": []string{"A"}, "filenames": []string{".."}}, "plain file name"},
		"filename too long":       {map[string]any{"id": "acme", "aliases": []string{"A"}, "filenames": []string{long}}, "plain file name"},
		"pattern star":            {map[string]any{"id": "acme", "aliases": []string{"A"}, "filenamePatterns": []string{"*"}}, "every file"},
		"pattern all files":       {map[string]any{"id": "acme", "aliases": []string{"A"}, "filenamePatterns": []string{"**/*"}}, "every file"},
		"pattern no extension":    {map[string]any{"id": "acme", "aliases": []string{"A"}, "filenamePatterns": []string{"Dockerfile.*"}}, "literal extension"},
		"pattern absolute":        {map[string]any{"id": "acme", "aliases": []string{"A"}, "filenamePatterns": []string{"/etc/*.conf"}}, "relative"},
		"pattern dotdot":          {map[string]any{"id": "acme", "aliases": []string{"A"}, "filenamePatterns": []string{"../*.acme"}}, "segments"},
		"pattern brace":           {map[string]any{"id": "acme", "aliases": []string{"A"}, "filenamePatterns": []string{"*.{a,b}"}}, "characters"},
		"grammar outside dirs":    {map[string]any{"id": "acme", "aliases": []string{"A"}, "grammar": "acme.json"}, "grammar"},
		"missing grammar":         {map[string]any{"id": "acme", "aliases": []string{"A"}, "grammar": "syntaxes/missing.json"}, "language grammar"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := writeLanguagePackage(t, []any{tc.entry}, acmeLanguageFiles())
			_, err := inspectInstallDirectory(root)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	valid := []map[string]any{
		{"id": "acme", "aliases": []string{"Acme"}},
		{"id": "json", "filenames": []string{".acmerc"}},
		{"id": "acme", "aliases": []string{"Acme"}, "filenamePatterns": []string{"Acmefile", "config/*.acme.json", "**/?.ac"}},
	}
	for index, entry := range valid {
		root := writeLanguagePackage(t, []any{entry}, nil)
		if _, err := inspectInstallDirectory(root); err != nil {
			t.Fatalf("valid[%d]: %v", index, err)
		}
	}
	root := writeLanguagePackage(t, []any{map[string]any{"id": "acme", "aliases": []string{"A"}}, map[string]any{"id": "acme", "aliases": []string{"B"}}}, nil)
	if _, err := inspectInstallDirectory(root); err == nil || !strings.Contains(err.Error(), "duplicate language id") {
		t.Fatalf("duplicate id error = %v", err)
	}
}

func checkGrammar(content string) error {
	root, err := parseLanguageJSON([]byte(content))
	if err != nil {
		return err
	}
	return validateMonarchGrammar(root)
}

func grammarWith(extra, tokenizer string) string {
	if extra != "" {
		extra += ","
	}
	return `{` + extra + `"tokenizer":` + tokenizer + `}`
}

func TestLanguagesMonarchGrammar(t *testing.T) {
	if err := checkGrammar(monarchGrammarFixture); err != nil {
		t.Fatalf("fixture grammar: %v", err)
	}
	deep := strings.Repeat(`[`, languageJSONDepthMax+1) + strings.Repeat(`]`, languageJSONDepthMax+1)
	manyStates := make([]string, 0, languageStatesMax+1)
	for index := 0; index <= languageStatesMax; index++ {
		manyStates = append(manyStates, fmt.Sprintf(`"s%d":[]`, index))
	}
	// Each level includes the next one twice: the expanded count doubles per level.
	var blowup []string
	for level := 0; level < 20; level++ {
		blowup = append(blowup, fmt.Sprintf(`"l%d":[{"include":"@l%d"},{"include":"@l%d"}]`, level, level+1, level+1))
	}
	blowup = append(blowup, `"l20":[["a","x"]]`)
	cases := map[string]struct {
		content string
		want    string
	}{
		"comment":               {`{"tokenizer":{"root":[]}} // x`, "strict JSON"},
		"trailing comma":        {`{"tokenizer":{"root":[],}}`, "strict JSON"},
		"duplicate key":         {`{"tokenizer":{"root":[]},"tokenizer":{"root":[]}}`, "duplicate key"},
		"duplicate state":       {`{"tokenizer":{"root":[],"root":[]}}`, "duplicate key"},
		"trailing value":        {`{"tokenizer":{"root":[]}} {}`, "exactly one JSON value"},
		"too deep":              {grammarWith(`"x":`+deep, `{"root":[]}`), "deeper"},
		"not an object":         {`[]`, "JSON object"},
		"missing tokenizer":     {`{"defaultToken":"x"}`, "tokenizer"},
		"no states":             {`{"tokenizer":{}}`, "states"},
		"too many states":       {`{"tokenizer":{` + strings.Join(manyStates, ",") + `}}`, "states"},
		"prototype state":       {`{"tokenizer":{"root":[],"constructor":[]}}`, "state name"},
		"array member state":    {`{"tokenizer":{"root":[],"push":[]}}`, "state name"},
		"bad state name":        {`{"tokenizer":{"a-b":[]}}`, "state name"},
		"unknown start":         {grammarWith(`"start":"nope"`, `{"root":[]}`), "start"},
		"bad attribute name":    {grammarWith(`"toString":"x"`, `{"root":[]}`), "attribute name"},
		"reserved attribute":    {grammarWith(`"stateNames":"x"`, `{"root":[]}`), "attribute name"},
		"log attribute":         {grammarWith(`"log":"x"`, `{"root":[]}`), "attribute name"},
		"number attribute":      {grammarWith(`"x":3`, `{"root":[]}`), "regex string or a list"},
		"state not a list":      {`{"tokenizer":{"root":{}}}`, "list of rules"},
		"short rule":            {`{"tokenizer":{"root":[["a"]]}}`, "[regex, action]"},
		"empty regex":           {`{"tokenizer":{"root":[["","x"]]}}`, "non-empty"},
		"state substitution":    {`{"tokenizer":{"root":[["$S2x","x"]]}}`, "$S"},
		"undefined attribute":   {`{"tokenizer":{"root":[["@nope","x"]]}}`, "undefined attribute"},
		"array attribute regex": {grammarWith(`"words":["a"]`, `{"root":[["@words","x"]]}`), "string attribute"},
		"expansion blowup":      {grammarWith(`"a":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx","b":"@a@a@a@a@a@a@a@a@a@a@a@a@a@a@a@a","c":"@b@b@b@b@b@b@b@b@b@b@b@b@b@b@b@b"`, `{"root":[["@c@c@c@c@c@c@c@c@c@c@c@c@c@c@c@c","x"]]}`), "expands beyond"},
		"missing next state":    {`{"tokenizer":{"root":[["a","x","@nope"]]}}`, "not a tokenizer state"},
		"missing next in token": {`{"tokenizer":{"root":[["a",{"token":"x","next":"nope"}]]}}`, "not a tokenizer state"},
		"missing switchTo":      {`{"tokenizer":{"root":[["a",{"token":"x","switchTo":"@pop"}]]}}`, "not a tokenizer state"},
		"group next":            {`{"tokenizer":{"root":[["a",["x"],"@pop"]]}}`, "with a next state"},
		"log action":            {`{"tokenizer":{"root":[["a",{"token":"x","log":"hi $0"}]]}}`, "log"},
		"unknown action key":    {`{"tokenizer":{"root":[["a",{"token":"x","foo":1}]]}}`, "unsupported action attribute"},
		"token and cases":       {`{"tokenizer":{"root":[["a",{"token":"x","cases":{"@default":"y"}}]]}}`, "cases action"},
		"no token":              {`{"tokenizer":{"root":[["a",{"next":"@pop"}]]}}`, "token or cases"},
		"bad bracket":           {`{"tokenizer":{"root":[["a",{"token":"x","bracket":"open"}]]}}`, "bracket"},
		"bad goBack":            {`{"tokenizer":{"root":[["a",{"token":"x","goBack":1.5}]]}}`, "goBack"},
		"bad nextEmbedded":      {`{"tokenizer":{"root":[["a",{"token":"x","nextEmbedded":"Not Id"}]]}}`, "nextEmbedded"},
		"dynamic guard regex":   {`{"tokenizer":{"root":[["(a)(b)",{"cases":{"$2~$1.*":"x"}}]]}}`, "matched text"},
		"dynamic guard dollar":  {`{"tokenizer":{"root":[["a",{"cases":{"~x$#":"x"}}]]}}`, "matched text"},
		"guard unknown words":   {`{"tokenizer":{"root":[["a",{"cases":{"@nope":"x"}}]]}}`, "word-list attribute"},
		"guard string attr":     {grammarWith(`"s":"x"`, `{"root":[["a",{"cases":{"!@s":"x"}}]]}`), "word-list attribute"},
		"cases too deep":        {`{"tokenizer":{"root":[["a",` + strings.Repeat(`{"cases":{"@default":`, languageCasesDepthMax) + `"x"` + strings.Repeat(`}}`, languageCasesDepthMax) + `]]}}`, "deeper"},
		"include missing":       {`{"tokenizer":{"root":[{"include":"@nope"}]}}`, "undefined state"},
		"include extra key":     {`{"tokenizer":{"root":[{"include":"@root","regex":"a"}]}}`, "include rule"},
		"include self":          {`{"tokenizer":{"root":[{"include":"@root"}]}}`, "include cycle"},
		"include cycle":         {`{"tokenizer":{"root":[{"include":"a"}],"a":[{"include":"b"}],"b":[{"include":"@a"}]}}`, "include cycle"},
		"include blowup":        {`{"tokenizer":{` + strings.Join(blowup, ",") + `}}`, "more than"},
		"rule object extra":     {`{"tokenizer":{"root":[{"regex":"a","action":"x","name":"n"}]}}`, "{\"regex\", \"action\"}"},
		"brackets equal":        {grammarWith(`"brackets":[["|","|","x"]]`, `{"root":[]}`), "must differ"},
		"brackets shape":        {grammarWith(`"brackets":[{"open":"(","close":")"}]`, `{"root":[]}`), "brackets entries"},
		"bad ignoreCase":        {grammarWith(`"ignoreCase":"yes"`, `{"root":[]}`), "boolean"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := checkGrammar(tc.content)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	accepted := []string{
		// Catastrophic regexes are the frontend's job; the host accepts them.
		`{"tokenizer":{"root":[["(a+)+b","x"]]}}`,
		`{"tokenizer":{"root":[["a",{"token":"x","next":"@push"}],["b",{"token":"y","switchTo":"@root.inner"}],["c","z","$S2"]]}}`,
		`{"tokenizer":{"root":[["(a)(b)",{"cases":{"$1==a":"x","$2~b|c":"y","~^x$":"z","@eos":"w","":"v"}}]]}}`,
		`{"tokenizer":{"root":[["a",{"token":"x","goBack":1,"bracket":"@close","nextEmbedded":"@pop"}],["@@x","y"]]}}`,
		`{"tokenizer":{"root":[{"include":"a"},{"include":"a"}],"a":[{"include":"b"}],"b":[["x","y"]]}}`,
		`{"start":"b","tokenizer":{"a":[],"b":[{"regex":"x","action":["a","b"]}]}}`,
	}
	for index, content := range accepted {
		if err := checkGrammar(content); err != nil {
			t.Fatalf("accepted[%d]: %v", index, err)
		}
	}
}

func TestLanguagesConfiguration(t *testing.T) {
	check := func(content string) error {
		root, err := parseLanguageJSON([]byte(content))
		if err != nil {
			return err
		}
		return validateLanguageConfiguration(root)
	}
	if err := check(testLanguageConfiguration); err != nil {
		t.Fatalf("configuration: %v", err)
	}
	cases := map[string]struct {
		content string
		want    string
	}{
		"onEnterRules":       {`{"onEnterRules":[]}`, "unsupported key"},
		"not an object":      {`[]`, "must be an object"},
		"line comment long":  {`{"comments":{"lineComment":"12345678901234567"}}`, "lineComment"},
		"block comment":      {`{"comments":{"blockComment":["/*"]}}`, "blockComment"},
		"pair shape":         {`{"brackets":[["{"]]}`, "[open, close]"},
		"pair too long":      {`{"brackets":[["123456789","}"]]}`, "characters"},
		"bracket object":     {`{"brackets":[{"open":"{","close":"}"}]}`, "[open, close]"},
		"notIn value":        {`{"autoClosingPairs":[{"open":"{","close":"}","notIn":["regex"]}]}`, "notIn"},
		"surrounding notIn":  {`{"surroundingPairs":[{"open":"{","close":"}","notIn":["string"]}]}`, "unsupported key"},
		"missing close":      {`{"autoClosingPairs":[{"open":"{"}]}`, "needs open and close"},
		"too many pairs":     {`{"brackets":[` + strings.TrimSuffix(strings.Repeat(`["a","b"],`, languagePairsMax+1), ",") + `]}`, "at most"},
		"folding regex":      {`{"folding":{"markers":{"start":"x"}}}`, "folding.markers.end"},
		"wordPattern object": {`{"wordPattern":{"pattern":"x"}}`, "wordPattern"},
		"indentation":        {`{"indentationRules":{"increaseIndentPattern":"x"}}`, "decreaseIndentPattern"},
		"duplicate key":      {`{"brackets":[],"brackets":[]}`, "duplicate key"},
		"jsonc":              {`{"brackets":[],}`, "strict JSON"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := check(tc.content)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLanguagesPerPluginBytesFinalize(t *testing.T) {
	entries := []contributionEntry{
		{Index: 0, ID: "a", Value: LanguageContribution{ID: "a", Grammar: make(json.RawMessage, languagePerPluginBytes-10)}},
		{Index: 1, ID: "b", Value: LanguageContribution{ID: "b", Configuration: make(json.RawMessage, 20)}},
		{Index: 2, ID: "c", Value: LanguageContribution{ID: "c", Configuration: make(json.RawMessage, 10)}},
	}
	kept, issues := finalizeLanguageEntries(nil, entries, nil)
	if len(kept) != 2 || kept[0].ID != "a" || kept[1].ID != "c" || len(issues) != 1 || issues[0].ID != "b" || !strings.Contains(issues[0].Message, "bytes in total") {
		t.Fatalf("kept = %+v issues = %+v", kept, issues)
	}
}

func TestLanguagesLenientInspectionReportsIssue(t *testing.T) {
	files := acmeLanguageFiles()
	files["syntaxes/bad.json"] = `{"tokenizer":{"root":[["a",{"token":"x","log":"$0"}]]}}`
	bad := acmeLanguageEntry()
	bad["id"], bad["grammar"] = "acme-bad", "syntaxes/bad.json"
	delete(bad, "configuration")
	root := writeLanguagePackage(t, []any{acmeLanguageEntry(), bad}, files)
	if _, err := inspectInstallDirectory(root); err == nil || !strings.Contains(err.Error(), "invalid languages contribution") {
		t.Fatalf("strict error = %v", err)
	}
	lenient, err := inspectPackageDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := lenient.Manifest.contribution(languagePointKey)
	if len(resolved.Entries) != 1 || len(resolved.Issues) != 1 || resolved.Issues[0].ID != "acme-bad" {
		t.Fatalf("resolved = %+v", resolved)
	}
}
