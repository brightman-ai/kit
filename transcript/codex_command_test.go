package transcript

import "testing"

func TestCodexExecCommandRealEnvelope(t *testing.T) {
	input := `const r = await tools.exec_command({
  cmd: "sed -n '1,240p' file.go && rg -n \"a{2}\" .",
  workdir: "/tmp/work",
  yield_time_ms: 10000,
  max_output_tokens: 20000
});
text(r.output);`
	got := codexExecCommand(input)
	want := `sed -n '1,240p' file.go && rg -n "a{2}" .`
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestCodexExecCommandGrammarAndFailure(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"comments and quoted key", `tools.exec_command({ /*x*/ "cmd": "printf '{x}'", other: call({a: 1}) })`, `printf '{x}'`},
		{"single quote", `tools.exec_command({cmd: 'rg \'quoted\''})`, `rg 'quoted'`},
		{"array", `tools.exec_command({cmd: ["git", "status", "--short"]})`, `git status --short`},
		{"nested decoy", `other({cmd:"wrong"}); tools.exec_command({meta:{cmd:"wrong"}, cmd:"right"})`, `right`},
		{"malformed", `tools.exec_command({cmd: "unterminated})`, ""},
		{"not exec command", `tools.other({cmd:"wrong"})`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexExecCommand(tt.input); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
