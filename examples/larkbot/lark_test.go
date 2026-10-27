package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

func TestThreadKeyOf(t *testing.T) {
	cases := []struct{ msg, root, parent, want string }{
		{"om_new", "", "", "om_new"},           // a new message starts a thread
		{"om_r", "om_root", "om_p", "om_root"}, // a reply: the root wins
		{"om_r", "", "om_p", "om_p"},           // no root: the parent
		{"om_r", "om_root", "", "om_root"},
	}
	for _, c := range cases {
		if got := threadKeyOf(c.msg, c.root, c.parent); got != c.want {
			t.Errorf("threadKeyOf(%q,%q,%q) = %q, want %q", c.msg, c.root, c.parent, got, c.want)
		}
	}
	if sessionIDFor(threadKeyOf("om_a", "", "")) == sessionIDFor(threadKeyOf("om_b", "", "")) {
		t.Fatal("two new messages must get two Sessions")
	}
	if sessionIDFor(threadKeyOf("om_b", "om_a", "om_a")) != sessionIDFor(threadKeyOf("om_a", "", "")) {
		t.Fatal("a reply must get its root's Session")
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("lark-om_abcdef12345678"); got != "12345678" {
		t.Fatalf("shortID = %q", got)
	}
	if got := shortID("abc"); got != "abc" {
		t.Fatalf("shortID = %q", got)
	}
}

func TestSessionIDFor(t *testing.T) {
	if got := sessionIDFor("om_abc123"); got != "lark-om_abc123" {
		t.Fatalf("plain id: %q", got)
	}
	a, b := sessionIDFor("../weird/chat.id"), sessionIDFor("../weird/chat.id")
	if a != b {
		t.Fatalf("not deterministic: %q %q", a, b)
	}
	if strings.ContainsAny(a, `/\.`) || !isBotSession(a) {
		t.Fatalf("unsafe id %q", a)
	}
	if sessionIDFor("a.b") == sessionIDFor("a.c") {
		t.Fatal("hash collision on distinct chats")
	}
}

func TestTextOf(t *testing.T) {
	cases := []struct {
		typ, content, want string
		ok                 bool
	}{
		{"text", `{"text":"hello"}`, "hello", true},
		{"text", `{"text":"@_user_1  hi there "}`, "hi there", true},
		{"text", `{"text":"@_user_1"}`, "", false},
		{"text", `not json`, "", false},
		{"image", `{"image_key":"x"}`, "", false},
	}
	for _, c := range cases {
		got, ok := textOf(c.typ, c.content)
		if got != c.want || ok != c.ok {
			t.Errorf("textOf(%q,%q) = %q,%v; want %q,%v", c.typ, c.content, got, ok, c.want, c.ok)
		}
	}
}

func TestSeenSet(t *testing.T) {
	s := newSeenSet(2)
	if !s.add("a") || s.add("a") || !s.add("b") || !s.add("c") || !s.add("a") {
		t.Fatal("seen set order/eviction wrong")
	}
}

type testCard struct {
	Header struct {
		Template string
		Title    struct{ Content string }
	}
	Elements []struct {
		Tag      string
		Content  string
		Elements []struct{ Tag, Content string }
	}
}

func parseCard(t *testing.T, content string) testCard {
	t.Helper()
	var c testCard
	if err := json.Unmarshal([]byte(content), &c); err != nil {
		t.Fatalf("card is not JSON: %v", err)
	}
	return c
}

var testInfo = cardInfo{Session: "lark-oc_1", Instance: "pod-a", Model: "demo"}

func TestCardShape(t *testing.T) {
	c := parseCard(t, cardContent(testInfo, " hi \n"))
	if len(c.Elements) != 2 || c.Elements[0].Tag != "markdown" || c.Elements[0].Content != "hi" {
		t.Fatalf("elements: %+v", c.Elements)
	}
	note := c.Elements[1]
	if note.Tag != "note" || note.Elements[0].Content != "session ark-oc_1 · instance pod-a · demo" {
		t.Fatalf("footer: %+v", note)
	}
	if c.Header.Template == "" || c.Header.Title.Content != "demo · pod-a" {
		t.Fatalf("header: %+v", c.Header)
	}
	if a, b := templateFor("pod-a"), templateFor("pod-a"); a != b {
		t.Fatal("colour not stable")
	}
}

func TestCardEscaping(t *testing.T) {
	nasty := "say \"hi\" \\ <b> `code` **x** \n line2   \x00 字"
	c := parseCard(t, cardContent(testInfo, nasty))
	if c.Elements[0].Content != strings.TrimSpace(nasty) {
		t.Fatalf("round trip: %q", c.Elements[0].Content)
	}
}

func TestCardTruncationAndEmpty(t *testing.T) {
	c := parseCard(t, cardContent(testInfo, strings.Repeat("字", maxReplyRunes+10)))
	if !strings.HasSuffix(c.Elements[0].Content, "(truncated)") {
		t.Fatal("long text not truncated")
	}
	if got := parseCard(t, cardContent(testInfo, "")).Elements[0].Content; got == "" {
		t.Fatal("empty text must say something")
	}
	if got := parseCard(t, replyCard(testInfo, gotato.RunFailed, "")).Elements[0].Content; !strings.Contains(got, "failed") {
		t.Fatalf("failed: %q", got)
	}
}

func TestInstanceLabel(t *testing.T) {
	t.Setenv("POD_NAME", "pod-x")
	if instanceLabel("mine", "r") != "mine" || instanceLabel("", "r") != "pod-x" {
		t.Fatal("override / POD_NAME")
	}
	t.Setenv("POD_NAME", "")
	if instanceLabel("", "r") == "" {
		t.Fatal("empty label")
	}
}

func TestAdvertiseAddr(t *testing.T) {
	for _, c := range [][3]string{
		{"", "127.0.0.1:8081", "http://127.0.0.1:8081"},
		{"", ":8080", "http://127.0.0.1:8080"},
		{"http://pod-a:8080/", "127.0.0.1:1", "http://pod-a:8080"},
		{"", "", ""},
	} {
		if got := advertiseAddr(c[0], c[1]); got != c[2] {
			t.Errorf("advertiseAddr(%q,%q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
