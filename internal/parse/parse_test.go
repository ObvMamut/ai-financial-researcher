package parse

import "testing"

func TestLastJSONBlock(t *testing.T) {
	text := "intro\n\n```json\n{\"a\":1}\n```\n\nmiddle\n\n```json\n{\"b\":2}\n```\ntail"
	got, ok := LastJSONBlock(text)
	if !ok {
		t.Fatal("LastJSONBlock: not found")
	}
	if got != `{"b":2}` {
		t.Fatalf("LastJSONBlock = %q, want the last block", got)
	}
}

func TestLastJSONBlockAbsent(t *testing.T) {
	if _, ok := LastJSONBlock("no fences here"); ok {
		t.Fatal("LastJSONBlock: reported a block in plain prose")
	}
	// An unterminated fence is not a block: the tail may be truncated output.
	if _, ok := LastJSONBlock("```json\n{\"a\":1}\n"); ok {
		t.Fatal("LastJSONBlock: accepted an unclosed fence")
	}
}

func TestReplaceLastJSONBlock(t *testing.T) {
	text := "intro\n\n```json\n{\"a\":1}\n```\n\nprose\n\n```json\n{\"b\":2}\n```\ntail"
	got, ok := ReplaceLastJSONBlock(text, `{"b":3}`)
	if !ok {
		t.Fatal("ReplaceLastJSONBlock: no block to replace")
	}
	want := "intro\n\n```json\n{\"a\":1}\n```\n\nprose\n\n```json\n{\"b\":3}\n```\ntail"
	if got != want {
		t.Fatalf("ReplaceLastJSONBlock =\n%q\nwant\n%q", got, want)
	}
	// The prose either side of the replaced block must survive untouched.
	if again, _ := LastJSONBlock(got); again != `{"b":3}` {
		t.Fatalf("round trip = %q, want the replacement", again)
	}
}

func TestReplaceLastJSONBlockAbsent(t *testing.T) {
	if _, ok := ReplaceLastJSONBlock("no fences here", `{"a":1}`); ok {
		t.Fatal("ReplaceLastJSONBlock: claimed to replace a block that does not exist")
	}
}

// A replacement containing `$` must land literally: regexp expansion would eat
// it, and JSON note fields carry dollar amounts all the time.
func TestReplaceLastJSONBlockLiteralDollar(t *testing.T) {
	text := "p\n\n```json\n{\"note\":\"old\"}\n```"
	got, ok := ReplaceLastJSONBlock(text, `{"note":"$1.2B revenue"}`)
	if !ok {
		t.Fatal("ReplaceLastJSONBlock: not found")
	}
	if raw, _ := LastJSONBlock(got); raw != `{"note":"$1.2B revenue"}` {
		t.Fatalf("literal $ mangled: %q", raw)
	}
}
