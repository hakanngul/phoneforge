package mobtest

import (
	"encoding/json"
	"strings"
	"testing"

	"pocketlab/internal/dsl"
)

func stepJSON(t *testing.T, line string) string {
	t.Helper()
	s, errs := Parse([]byte(line))
	if len(errs) > 0 {
		t.Fatalf("%q: %v", line, errs)
	}
	if len(s.Steps) != 1 {
		t.Fatalf("%q: %d steps", line, len(s.Steps))
	}
	b, _ := json.Marshal(s.Steps[0])
	return string(b)
}

func TestParseDocExample(t *testing.T) {
	src := `# Param: email = user@example.com
# Alerts: accept

app "com.example.app" fresh
wait_for ~"Sign in" timeout:10000
type ~"Email" → "${email}" clear
type_secret ~"Password" → "qa-password"
tap "Sign in" type:button
assert_exists ~"Welcome" timeout:5000
wait_stable timeout:3000
observe
`
	s, errs := Parse([]byte(src))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if s.Alerts != "accept" || s.Params["email"].Default != "user@example.com" || !s.Params["email"].Has {
		t.Fatalf("%+v %+v", s.Alerts, s.Params)
	}
	want := []string{
		`"action":"open_app"`, `"action":"wait_for"`, `"action":"type"`, `"action":"type_secret"`,
		`"action":"tap"`, `"action":"assert_exists"`, `"action":"wait_for"`, `"action":"observe"`,
	}
	if len(s.Steps) != len(want) {
		t.Fatalf("%d steps", len(s.Steps))
	}
	for i, w := range want {
		b, _ := json.Marshal(s.Steps[i])
		if !strings.Contains(string(b), w) {
			t.Errorf("step %d = %s, want %s", i, b, w)
		}
	}
	if err := dsl.Validate(s); err != nil {
		t.Fatal(err)
	}
	if !s.Steps[0].Fresh || s.Steps[0].BundleID != "com.example.app" || s.Steps[1].TimeoutMs != 10000 ||
		s.Steps[2].Text != "${email}" || !s.Steps[2].ClearFirst || s.Steps[3].SecretID != "qa-password" ||
		s.Steps[4].Selector.Type != "button" || !s.Steps[6].Stable || s.Steps[6].TimeoutMs != 3000 {
		t.Fatalf("%+v", s.Steps)
	}
}

func TestParseLineForms(t *testing.T) {
	tests := []struct{ line, want string }{
		{`tap "Save"`, `{"action":"tap","selector":{"text":"Save"}}`},
		{`tap ~"Sa"`, `{"action":"tap","selector":{"textContains":"Sa"}}`},
		{`tap /"^Sa.e$"/`, `{"action":"tap","selector":{"textRegex":"^Sa.e$"}}`},
		{`tap /"^sa"/i`, `{"action":"tap","selector":{"textRegex":"(?i)^sa"}}`},
		{`tap #save_btn`, `{"action":"tap","selector":{"accessibilityId":"save_btn"}}`},
		{`tap "Item" [2]`, `{"action":"tap","selector":{"text":"Item","index":2}}`},
		{`tap "Save" bounds:top_half enabled:true`, `{"action":"tap","selector":{"text":"Save","enabled":true,"boundsHint":"top_half"}}`},
		{`tap type:button near "Email" direction:below`, `{"action":"tap","selector":{"type":"button","near":{"text":"Email","direction":"below"}}}`},
		{`tap "Edit" parent_of:"Delete"`, `{"action":"tap","selector":{"text":"Edit","parentOf":{"text":"Delete"}}}`},
		{`tap value:"x" value_contains:"@"`, `{"action":"tap","selector":{"value":"x","valueContains":"@"}}`},
		{`tap 100,200`, `{"action":"tap","coords":{"x":100,"y":200}}`},
		{`tap 50%,25%`, `{"action":"tap","coords":{"xPercent":50,"yPercent":25}}`},
		{`tap "Say \"hi\""`, `{"action":"tap","selector":{"text":"Say \"hi\""}}`},
		{`tap "Continue" on_fail:retry retries:3 retry_delay:1000`, `{"action":"tap","selector":{"text":"Continue"},"onFail":{"strategy":"retry"},"retries":3,"retryDelayMs":1000}`},
		{`tap "Not now" on_fail:skip`, `{"action":"tap","selector":{"text":"Not now"},"onFail":{"strategy":"skip"}}`},
		{`tap "x" on_fail:replan`, `{"action":"tap","selector":{"text":"x"},"onFail":{"strategy":"replan"}}`},
		{`long_press "Message" duration:1500`, `{"action":"long_press","selector":{"text":"Message"},"durationMs":1500}`},
		{`double_tap "Photo"`, `{"action":"double_tap","selector":{"text":"Photo"}}`},
		{`pinch ~"Map" scale:0.5`, `{"action":"pinch","selector":{"textContains":"Map"},"scale":0.5}`},
		{`swipe up distance:medium`, `{"action":"swipe","direction":"up","distance":"medium"}`},
		{`swipe from:200,700 to:200,200 duration:400`, `{"action":"swipe","durationMs":400,"fromCoords":{"x":200,"y":700},"toCoords":{"x":200,"y":200}}`},
		{`scroll down to "Privacy" max_scrolls:10`, `{"action":"scroll","toElement":{"predicate":{"text":"Privacy"}},"direction":"down","maxScrolls":10}`},
		{`scroll right in type:scrollview amount:small`, `{"action":"scroll","selector":{"type":"scrollview"},"direction":"right","amount":"small"}`},
		{`scroll down`, `{"action":"scroll","direction":"down"}`},
		{`drag "Item 1" to "Trash"`, `{"action":"drag","from":{"predicate":{"text":"Item 1"}},"toElement":{"predicate":{"text":"Trash"}}}`},
		{`drag 100,200 to 300,400 press_duration:500 hold_duration:200 duration:800`, `{"action":"drag","durationMs":800,"fromCoords":{"x":100,"y":200},"toCoords":{"x":300,"y":400},"pressDurationMs":500,"holdDurationMs":200}`},
		{`type "hello"`, `{"action":"type","text":"hello"}`},
		{`type "Email" -> "a@b.c" clear dismiss`, `{"action":"type","selector":{"text":"Email"},"text":"a@b.c","clearFirst":true,"dismissKeyboard":true}`},
		{`type "Email"→"a@b.c"`, `{"action":"type","selector":{"text":"Email"},"text":"a@b.c"}`},
		{`clear`, `{"action":"clear"}`},
		{`clear "Email"`, `{"action":"clear","selector":{"text":"Email"}}`},
		{`hide_keyboard`, `{"action":"hide_keyboard"}`},
		{`copy_text ~"Code" store_as:code`, `{"action":"copy_text","selector":{"textContains":"Code"},"storeAs":"code"}`},
		{`paste_text`, `{"action":"paste_text"}`},
		{`press_key enter`, `{"action":"press_key","key":"enter"}`},
		{`navigate home`, `{"action":"navigate","target":"home"}`},
		{`navigate "https://example.com"`, `{"action":"navigate","target":"https://example.com"}`},
		{`toggle type:switch near ~"Wi-Fi" on`, `{"action":"toggle","selector":{"type":"switch","near":{"textContains":"Wi-Fi"}},"state":"on"}`},
		{`select_picker "Month" → "May"`, `{"action":"select_picker","selector":{"text":"Month"},"value":"May"}`},
		{`delay 1000`, `{"action":"delay","durationMs":1000}`},
		{`wait_for "Welcome" timeout:5000`, `{"action":"wait_for","selector":{"text":"Welcome"},"timeoutMs":5000}`},
		{`wait_stable`, `{"action":"wait_for","stable":true}`},
		{`checkpoint "login"`, `{"action":"checkpoint","name":"login"}`},
		{`open_link "myapp://profile/42"`, `{"action":"open_link","url":"myapp://profile/42"}`},
		{`app "com.example.app" fresh arg:"-UITesting" env:"API_URL=http://localhost:3000"`, `{"action":"open_app","bundleId":"com.example.app","fresh":true,"arguments":["-UITesting"],"environment":{"API_URL":"http://localhost:3000"}}`},
		{`kill_app "com.example.app"`, `{"action":"kill_app","bundleId":"com.example.app"}`},
		{`set_location 37.77,-122.42`, `{"action":"set_location","lat":37.77,"lon":-122.42}`},
		{`reset_location`, `{"action":"reset_location"}`},
		{`set_permissions com.x camera=allow location=deny`, `{"action":"set_permissions","bundleId":"com.x","permissions":{"camera":"allow","location":"deny"}}`},
		{`biometrics match faceid`, `{"action":"biometrics","state":"match","sensor":"faceid"}`},
		{`fold closed`, `{"action":"fold","state":"closed"}`},
		{`fold 90`, `{"action":"fold","angle":90}`},
		{`siri "what time is it"`, `{"action":"siri","prompt":"what time is it"}`},
		{`assert_count type:cell expected:5`, `{"action":"assert_count","selector":{"type":"cell"},"expected":5}`},
		{`assert_not_exists "Error" timeout:2000`, `{"action":"assert_not_exists","selector":{"text":"Error"},"timeoutMs":2000}`},
		{`extract code from ~"Code" regex:"(\\d+)"`, `{"action":"extract","selector":{"textContains":"Code"},"extractKey":"code","extractKind":"text","extractRegex":"(\\d+)"}`},
		{`extract snap screenshot`, `{"action":"extract","extractKey":"snap","extractKind":"screenshot"}`},
		{`extract greeting = "hello"`, `{"action":"extract","text":"hello","extractKey":"greeting","extractKind":"literal"}`},
		{`eval "vars.x = 1"`, `{"action":"eval_script","script":"vars.x = 1"}`},
		{`script "./seed.js"`, `{"action":"run_script","scriptPath":"./seed.js"}`},
		{`screenshot`, `{"action":"screenshot"}`},
		{`observe ui_tree`, `{"action":"observe","include":["ui_tree"]}`},
		{`assert_visible "x"`, `{"action":"assert_exists","selector":{"text":"x"}}`},
		{`tap "x" id:step1 platform:ios`, `{"action":"tap","id":"step1","platform":"ios","selector":{"text":"x"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			got := stepJSON(t, tc.line)
			if got != tc.want {
				t.Fatalf("\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestParseBlocksAndPlatforms(t *testing.T) {
	src := `# comment
if_exists "Allow" {
  tap "Allow"
} else {
  checkpoint "no_prompt"
}
repeat 3 times {
  tap "Add"
}
repeat while ~"Loading" max:20 {
  delay 500
}
repeat until "Done" type:button {
  scroll down
}
repeat while js:"vars.n < 3" {
  delay 1
}
# ios
tap "iOS only"
# end
#[android]
tap "Android only"
#[end]
`
	s, errs := Parse([]byte(src))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(s.Steps) != 7 {
		t.Fatalf("%d steps", len(s.Steps))
	}
	if len(s.Steps[0].Then) != 1 || len(s.Steps[0].Else) != 1 || s.Steps[0].Else[0].Name != "no_prompt" {
		t.Fatalf("%+v", s.Steps[0])
	}
	if s.Steps[1].Times != "3" || len(s.Steps[1].Body) != 1 {
		t.Fatalf("%+v", s.Steps[1])
	}
	if s.Steps[2].While == nil || s.Steps[2].While.TextContains != "Loading" || s.Steps[2].MaxIterations != 20 {
		t.Fatalf("%+v", s.Steps[2])
	}
	if s.Steps[3].Until == nil || s.Steps[3].Until.Type != "button" || s.Steps[3].Until.Text != "Done" {
		t.Fatalf("%+v", s.Steps[3])
	}
	if s.Steps[4].Condition != "vars.n < 3" {
		t.Fatalf("%+v", s.Steps[4])
	}
	if s.Steps[5].Platform != "ios" || s.Steps[6].Platform != "android" {
		t.Fatalf("%+v %+v", s.Steps[5], s.Steps[6])
	}
	if err := dsl.Validate(s); err != nil {
		t.Fatal(err)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []ParseError
	}{
		{"unknown action", "tap \"a\"\nzap \"b\"\n", []ParseError{{2, `unknown action "zap"`}}},
		{"unterminated string", "tap \"abc\n", []ParseError{{1, "unterminated string"}}},
		{"bad setting", "tap \"a\" timeout:abc\n", []ParseError{{1, "timeout must be an integer"}}},
		{"unknown setting", "tap \"a\" bogus:1\n", []ParseError{{1, "unknown setting bogus:"}}},
		{"bad on_fail", "tap \"a\" on_fail:explode\n", []ParseError{{1, "on_fail must be"}}},
		{"type without arrow", "type Email\n", []ParseError{{1, "expected: type"}}},
		{"unexpected close", "tap \"a\"\n}\n", []ParseError{{2, "unexpected '}'"}}},
		{"missing close", "if_exists \"a\" {\n  tap \"b\"\n", []ParseError{{1, "missing its closing"}}},
		{"block on plain action", "tap \"a\" {\n}\n", []ParseError{{1, "does not take a block"}}},
		{"if_exists needs block", "if_exists \"a\"\n", []ParseError{{1, "needs a block"}}},
		{"two texts", "tap \"a\" \"b\"\n", []ParseError{{1, "more than one quoted text"}}},
		{"bad alerts", "# Alerts: maybe\n", []ParseError{{1, "# Alerts must be"}}},
		{"end without block", "# end\n", []ParseError{{1, "without an open platform block"}}},
		{"unclosed platform", "tap \"a\"\n# ios\ntap \"b\"\n", []ParseError{{2, "missing '# end'"}}},
		{"nested platform", "# ios\n# android\n# end\n", []ParseError{{2, "cannot be nested"}}},
		{"bad regex", "tap /\"(\"/\n", []ParseError{{1, "invalid text_regex"}}},
		{"delay needs number", "delay soon\n", []ParseError{{1, "positive integer"}}},
		{"run outside project", "run \"./a.mob\"\n", []ParseError{{1, "only available inside a test project"}}},
		{"drag incomplete", "drag \"a\"\n", []ParseError{{1, "drag needs a source"}}},
		{"repeat bad", "repeat soon {\n}\n", []ParseError{{1, "repeat needs"}}},
		{"near without target", "tap near\n", []ParseError{{1, "near needs"}}},
		{"else alone", "else {\n}\n", []ParseError{{1, "'else' must follow"}}},
		{"two errors", "zap\ntap \"ok\"\nzip\n", []ParseError{{1, "unknown action"}, {3, "unknown action"}}},
		{"keyword only", "\"x\"\n", []ParseError{{1, "must start with an action keyword"}}},
		{"json-only action", "audit foo\n", []ParseError{{1, "unexpected"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := Parse([]byte(tc.src))
			if len(errs) != len(tc.want) {
				t.Fatalf("errors = %v", errs)
			}
			for i, w := range tc.want {
				if errs[i].Line != w.Line || !strings.Contains(errs[i].Message, w.Message) {
					t.Errorf("error %d = %+v, want line %d containing %q", i, errs[i], w.Line, w.Message)
				}
			}
		})
	}
}

func TestParseRunInclude(t *testing.T) {
	files := map[string]string{
		"login.mob": "# Param: email = default@x.com\n# Param: pw = secret\ntype \"Email\" → \"${email}\"\ntype \"Password\" → \"${pw}\"\nif_exists \"X\" {\n  tap \"${email}\"\n}\n",
		"loop.mob":  "run \"./loop.mob\"\n",
		"bad.mob":   "zap\n",
	}
	read := func(n string) ([]byte, error) {
		if s, ok := files[n]; ok {
			return []byte(s), nil
		}
		return nil, errNoFile
	}
	opts := ParseOptions{ReadFile: read}
	s, errs := ParseWith([]byte("app \"com.x\"\nrun \"./login.mob\" email=\"me@x.com\"\n"), opts)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(s.Steps) != 4 || s.Steps[1].Text != "me@x.com" || s.Steps[2].Text != "${pw}" || s.Steps[3].Then[0].Selector.Text != "me@x.com" {
		b, _ := json.Marshal(s.Steps)
		t.Fatalf("%s", b)
	}
	if sp, ok := s.Params["pw"]; !ok || sp.Default != "secret" || len(s.Params) != 1 {
		t.Fatalf("params = %+v", s.Params)
	}
	for _, tc := range []struct{ src, msg string }{
		{`run "./loop.mob"`, "recursive"},
		{`run "./bad.mob"`, "in bad.mob"},
		{`run "./missing.mob"`, "no such file"},
		{`run "../etc/passwd"`, "same project"},
		{`run "sub/x.mob"`, "same project"},
		{`run login.mob`, "quoted path"},
		{`run "./login.mob" oops`, "parameters look like"},
	} {
		_, errs := ParseWith([]byte(tc.src), opts)
		if len(errs) != 1 || !strings.Contains(errs[0].Message, tc.msg) {
			t.Errorf("%s -> %v", tc.src, errs)
		}
	}
	// IgnoreRun accepts run lines for validation only
	if s, errs := ParseWith([]byte(`run "./x.mob"`), ParseOptions{IgnoreRun: true}); len(errs) != 0 || len(s.Steps) != 0 {
		t.Fatalf("%v %v", s.Steps, errs)
	}
}

type fileErr string

func (e fileErr) Error() string { return string(e) }

const errNoFile = fileErr("no such file")

func TestFormatRoundTrip(t *testing.T) {
	src := `# Param: email = user@example.com
# Param: pw
# Alerts: dismiss

app "com.example.app" fresh arg:"-UITesting" env:"A=b c"
wait_for ~"Sign in" timeout:10000
type ~"Email" → "${email}" clear dismiss
type_secret #pw_field → "secret-id"
tap "Sign in" type:button near "Email" direction:below max_distance:300 [1]
tap /"^Go"/i bounds:bottom_half enabled:true
tap 50%,25% on_fail:retry retries:3 retry_delay:500
tap "Edit" parent_of:~"Del"
long_press "Message" duration:1500
swipe up distance:full duration:200
swipe from:1,2 to:3,4
scroll down to "Privacy" fully_visible max_scrolls:4 amount:full
scroll right in type:scrollview
drag "A" to 10,20
toggle type:switch near ~"Wi-Fi" on
select_picker "Month" → "May"
if_exists "Allow" {
  tap "Allow"
  repeat 2 times {
    delay 100
  }
} else {
  checkpoint "no \"prompt\""
}
repeat until "Done" type:button max:5 {
  scroll down
}
repeat while js:"vars.n < 3" {
  delay 1
}
assert_count type:cell expected:5
assert_not_exists "Error" timeout:2000 message:"must be gone"
assert_property "Save" property:enabled expected_value:true
assert_screen_changed threshold:30
extract code from ~"Code" regex:"(\d+)"
extract snap screenshot
extract greeting = "hello"
set_location 37.77,-122.42
set_permissions com.x camera=allow location=deny
biometrics match faceid
fold 90
observe ui_tree screenshot
press_key enter
navigate home
hide_keyboard
wait_stable timeout:3000
# ios
open_link "myapp://x"
# end
# android
tap "Back" id:b1
# end
`
	s1, errs := Parse([]byte(src))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	out := Format(s1)
	if strings.Contains(string(out), "not representable") {
		t.Fatalf("something was not representable:\n%s", out)
	}
	s2, errs := Parse(out)
	if len(errs) > 0 {
		t.Fatalf("%v\n%s", errs, out)
	}
	j1, _ := json.Marshal(s1)
	j2, _ := json.Marshal(s2)
	if string(j1) != string(j2) {
		t.Fatalf("round trip differs\n%s\n---\n%s\n---\n%s", j1, j2, out)
	}
	if string(Format(s2)) != string(out) {
		t.Fatal("Format is not stable")
	}
}

func TestFormatNotRepresentable(t *testing.T) {
	s := dsl.Script{Steps: []dsl.Step{
		{Action: "launch_app", BundleID: "com.x"},
		{Action: "swipe", X1: 1, Y1: 2, X2: 3, Y2: 4}, // raw x1..y2 has no .mob form
		{Action: "perform_actions"},
		{Action: "tap", Selector: &dsl.Selector{Text: "a", Near: &dsl.Near{Selector: dsl.Selector{Text: "b", Type: "x"}}}},
	}}
	out := string(Format(s))
	if strings.Count(out, "# [not representable in .mob]") != 3 || !strings.Contains(out, `app "com.x"`) {
		t.Fatalf("%s", out)
	}
	if _, errs := Parse([]byte(out)); len(errs) > 0 {
		t.Fatal(errs)
	}
}
