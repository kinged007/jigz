package hubproto

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type message interface{ Validate() error }

var messages = map[string]func() message{
	"enroll.request":  func() message { return new(EnrollRequest) },
	"enroll.response": func() message { return new(EnrollResponse) },
	"config.response": func() message { return new(ConfigResponse) },
	"status.request":  func() message { return new(StatusRequest) },
	"status.response": func() message { return new(StatusResponse) },
	"runs.request":    func() message { return new(RunsRequest) },
	"runs.response":   func() message { return new(RunsResponse) },
	"ws.command":      func() message { return new(Command) },
	"ws.ack":          func() message { return new(Ack) },
	"error":           func() message { return new(ErrorBody) },
}

var enums = map[string][]string{
	"runs.request:events[].type":          {"started", "finished"},
	"runs.request:events[].item.kind":     {"issue", "pr"},
	"runs.request:events[].outcome":       {"succeeded", "failed_agent", "failed_workspace", "failed_limit", "cancelled", "timeout", "handoff_withheld"},
	"status.request:loops[].state":        {"running", "sleeping", "stopped"},
	"status.request:loops[].detail":       {"paused", "held", "disabled", "exited", "stopping"},
	"config.response:projects[].provider": {"github"},
	"ws.command:type":                     {"start", "stop", "drain", "restart_when_idle", "pause", "resume", "wake", "reload_config", "session.open", "session.message", "session.close", "task.run"},
	"ws.ack:error.code":                   {"unsupported", "invalid", "failed"},
	"error:code":                          {"invalid", "unsupported_version", "unauthorized", "forbidden", "not_found", "unavailable", "internal"},
}

func constants[T ~string](vs ...T) []string {
	var out []string
	for _, v := range vs {
		out = append(out, string(v))
	}
	return out
}

func TestConstantsMatchEnums(t *testing.T) {
	got := map[string][]string{
		"runs.request:events[].type":          constants(EventStarted, EventFinished),
		"runs.request:events[].item.kind":     constants(KindIssue, KindPR),
		"runs.request:events[].outcome":       constants(outcomes...),
		"status.request:loops[].state":        constants(states...),
		"status.request:loops[].detail":       constants(details[1:]...),
		"config.response:projects[].provider": {ProviderGitHub},
		"ws.command:type":                     constants(commands...),
		"ws.ack:error.code":                   constants(ackCodes...),
		"error:code":                          constants(errCodes...),
	}
	for key, want := range enums {
		if !slices.Equal(got[key], want) {
			t.Errorf("%s: constants %v, want %v", key, got[key], want)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func schemaDir(t *testing.T) string {
	return filepath.Join(repoRoot(t), "schemas", "hub-protocol", "v0")
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

func jsonFields(t *testing.T, typ reflect.Type) map[string]bool {
	t.Helper()
	fields := map[string]bool{}
	for i := range typ.NumField() {
		name, opts, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			t.Errorf("%s.%s has no json name", typ, typ.Field(i).Name)
			continue
		}
		fields[name] = strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero")
	}
	return fields
}

func fieldType(typ reflect.Type, name string) reflect.Type {
	for i := range typ.NumField() {
		if n, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ","); n == name {
			return typ.Field(i).Type
		}
	}
	return nil
}

func elem(typ reflect.Type) reflect.Type {
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
		typ = typ.Elem()
	}
	return typ
}

func walk(t *testing.T, node map[string]any, typ reflect.Type, path, schema string, seen map[string][]string) {
	t.Helper()
	if e, ok := node["enum"].([]any); ok {
		var vals []string
		for _, v := range e {
			vals = append(vals, v.(string))
		}
		seen[schema+":"+path] = vals
	}
	if items, ok := node["items"].(map[string]any); ok {
		walk(t, items, typ, path+"[]", schema, seen)
		return
	}
	props, ok := node["properties"].(map[string]any)
	typ = elem(typ)
	if !ok || typ.Kind() != reflect.Struct {
		return
	}
	required := map[string]bool{}
	if r, ok := node["required"].([]any); ok {
		for _, n := range r {
			required[n.(string)] = true
		}
	}
	fields := jsonFields(t, typ)
	for name := range props {
		omit, ok := fields[name]
		where := schema + ":" + path + "." + name
		switch {
		case !ok:
			t.Errorf("%s: property has no field in %s", where, typ)
		case required[name] && omit:
			t.Errorf("%s: required property has omitempty", where)
		case !required[name] && !omit:
			t.Errorf("%s: optional property has no omitempty", where)
		}
	}
	for name := range fields {
		if _, ok := props[name]; !ok {
			t.Errorf("%s:%s: field %q of %s is not a property", schema, path, name, typ)
		}
	}
	for name, p := range props {
		if ft := fieldType(typ, name); ft != nil {
			child := strings.TrimPrefix(path+"."+name, ".")
			walk(t, p.(map[string]any), ft, child, schema, seen)
		}
	}
}

func TestSchemasMatchTypes(t *testing.T) {
	seen := map[string][]string{}
	for name, ctor := range messages {
		t.Run(name, func(t *testing.T) {
			schema := readJSON(t, filepath.Join(schemaDir(t), name+".schema.json"))
			walk(t, schema, reflect.TypeOf(ctor()), "", name, seen)
			if defs, ok := schema["$defs"].(map[string]any); ok {
				if len(defs) != 1 || defs["loop_args"] == nil {
					t.Fatalf("unmapped $defs in %s", name)
				}
				walk(t, defs["loop_args"].(map[string]any), reflect.TypeOf(LoopArgs{}), "", name+"$loop_args", seen)
			}
		})
	}
	for key, want := range enums {
		if !slices.Equal(seen[key], want) {
			t.Errorf("%s: schema enum %v, want %v", key, seen[key], want)
		}
	}
	for key := range seen {
		if _, ok := enums[key]; !ok {
			t.Errorf("schema enum %s has no expected list", key)
		}
	}
}

func schemaOf(file string) string {
	best := ""
	stem := strings.TrimSuffix(file, ".json")
	for name := range messages {
		if (stem == name || strings.HasPrefix(stem, name+".")) && len(name) > len(best) {
			best = name
		}
	}
	return best
}

func decodeStrict(data []byte, v message) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func exampleFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(schemaDir(t), dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples in %s: %v", dir, err)
	}
	return files
}

func TestValidExamplesRoundTrip(t *testing.T) {
	for _, file := range exampleFiles(t, "examples") {
		name := filepath.Base(file)
		t.Run(name, func(t *testing.T) {
			ctor := messages[schemaOf(name)]
			if ctor == nil {
				t.Fatalf("no schema for %s", name)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			msg := ctor()
			if err := decodeStrict(data, msg); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if err := msg.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
			encoded, err := json.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Errorf("re-encoded %s, want %s", encoded, data)
			}
		})
	}
}

func TestInvalidExamplesRejected(t *testing.T) {
	for _, file := range exampleFiles(t, "examples/invalid") {
		name := filepath.Base(file)
		t.Run(name, func(t *testing.T) {
			ctor := messages[schemaOf(name)]
			if ctor == nil {
				t.Fatalf("no schema for %s", name)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			msg := ctor()
			if err := decodeStrict(data, msg); err != nil {
				return
			}
			if err := msg.Validate(); err == nil {
				t.Error("accepted by decode and Validate")
			}
		})
	}
}

func TestValidate(t *testing.T) {
	when := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	started := RunEvent{ID: "e", Type: EventStarted, Workflow: "issues", Item: Item{"a/b", 1, KindIssue}, Attempt: 1, StartedAt: when}
	finished := started
	finished.Type, finished.FinishedAt, finished.Outcome, finished.Usage = EventFinished, &when, OutcomeSucceeded, &Usage{}
	cases := []struct {
		name string
		msg  message
		ok   bool
	}{
		{"started event", started, true},
		{"finished event", finished, true},
		{"finished without usage", func() message { e := finished; e.Usage = nil; return e }(), false},
		{"started with outcome", func() message { e := started; e.Outcome = OutcomeTimeout; return e }(), false},
		{"unknown outcome", func() message { e := finished; e.Outcome = "x"; return e }(), false},
		{"ack ok", Ack{ID: "1", Type: "ack", OK: true}, true},
		{"ack failed with error", Ack{ID: "1", Type: "ack", Error: &AckError{AckFailed, "x"}}, true},
		{"ack failed without error", Ack{ID: "1", Type: "ack"}, false},
		{"command resume", Command{ID: "1", Type: CmdResume, Args: json.RawMessage(`{}`)}, true},
		{"command start all loops", Command{ID: "1", Type: CmdStart, Args: json.RawMessage(`{}`)}, true},
		{"command stop project", Command{ID: "1", Type: CmdStop, Args: json.RawMessage(`{"project":"a/b"}`)}, true},
		{"command without args", Command{ID: "1", Type: CmdResume}, false},
		{"command args not object", Command{ID: "1", Type: CmdTaskRun, Args: json.RawMessage(`[]`)}, false},
		{"command loop args unknown field", Command{ID: "1", Type: CmdStop, Args: json.RawMessage(`{"x":1}`)}, false},
		{"error with range", ErrorBody{Code: ErrUnsupportedVersion, Supported: &Supported{0, 0}}, true},
		{"error range on other code", ErrorBody{Code: ErrInternal, Supported: &Supported{0, 0}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.msg.Validate(); (err == nil) != c.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, c.ok)
			}
		})
	}
}
