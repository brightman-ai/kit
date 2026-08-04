package transcript

import "testing"

// Each case here is a shard name observed in a live ~/.claude/projects, paired with the cwd its
// own transcript records. They are fixtures copied from reality rather than derived from the
// encoder, which is the point: the encoder's job is to reproduce claude's naming, so a test
// written from the encoder's own rule would only prove it agrees with itself.
func TestEncodeProjectDirRealWorldShards(t *testing.T) {
	cases := []struct{ cwd, shard string }{
		// Plain path — the case that always worked.
		{"/Users/anthony/code/stwork/deepwork-terminal", "-Users-anthony-code-stwork-deepwork-terminal"},
		// Underscores. Every one of these used to resolve to a directory that does not exist, so
		// the pane owning it could never find its transcript.
		{"/Users/anthony/code/claude_remote", "-Users-anthony-code-claude-remote"},
		{"/Users/anthony/code/mytool/update_discourse_cert", "-Users-anthony-code-mytool-update-discourse-cert"},
		{"/Users/anthony/code/stwork/ppt_workbench", "-Users-anthony-code-stwork-ppt-workbench"},
		// A dot directory: '/.' collapses to '--'.
		{"/home/u/.deepwork/ws", "-home-u--deepwork-ws"},
		// All three classes at once.
		{"/home/u/.config/my_app.v2", "-home-u--config-my-app-v2"},
	}
	for _, tc := range cases {
		if got := EncodeProjectDir(tc.cwd); got != tc.shard {
			t.Errorf("EncodeProjectDir(%q)\n got %q\nwant %q", tc.cwd, got, tc.shard)
		}
	}
}

// The encoding is lossy on purpose — it mirrors claude, and claude collapses these characters.
// Pinning the collision documents that we know, so nobody "fixes" it into a scheme claude does
// not use: writing to a directory claude never reads is strictly worse than sharing one.
func TestEncodeProjectDirIsDeliberatelyLossy(t *testing.T) {
	if EncodeProjectDir("/a/b_c") != EncodeProjectDir("/a/b-c") {
		t.Error("underscore and dash no longer collide — this encoder must match claude's naming, not improve on it")
	}
}
