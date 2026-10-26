package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

// Pure helpers: no network, no Gotato runtime. They are unit tested.

const sessionPrefix = "lark-"

var plainID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// threadKeyOf is the key of the message thread a message belongs to: the root
// message of its reply chain when it is a reply (root_id, else parent_id), and
// the message itself when it starts a new thread. The key is always a message_id.
func threadKeyOf(messageID, rootID, parentID string) string {
	switch {
	case rootID != "":
		return rootID
	case parentID != "":
		return parentID
	}
	return messageID
}

// sessionIDFor maps a thread key to a Gotato Session ID, deterministically.
// A key made of plain characters is kept readable ("lark-om_abc"); any
// other is hashed, so the ID never holds a path separator or a dot.
func sessionIDFor(threadKey string) string {
	if plainID.MatchString(threadKey) {
		return sessionPrefix + threadKey
	}
	sum := sha256.Sum256([]byte(threadKey))
	return sessionPrefix + "h" + hex.EncodeToString(sum[:16])
}

// shortID is the last 8 characters of a Session ID, for the card footer.
func shortID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

// isBotSession says whether a Session belongs to this bot (its ID has our prefix).
func isBotSession(id string) bool { return strings.HasPrefix(id, sessionPrefix) }

var mentionPlaceholder = regexp.MustCompile(`@_user_\d+`)

// textOf extracts the user's text from a Lark message. ok is false for any
// message type but "text", or for a text that is empty once mentions are removed.
func textOf(messageType, content string) (text string, ok bool) {
	if messageType != "text" {
		return "", false
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &body); err != nil {
		return "", false
	}
	text = strings.TrimSpace(mentionPlaceholder.ReplaceAllString(body.Text, ""))
	return text, text != ""
}

// seenSet remembers recent message IDs, because Lark may deliver an event twice.
type seenSet struct {
	order []string
	set   map[string]struct{}
	max   int
}

func newSeenSet(limit int) *seenSet { return &seenSet{set: map[string]struct{}{}, max: limit} }

// add reports whether id was new.
func (s *seenSet) add(id string) bool {
	if _, dup := s.set[id]; dup {
		return false
	}
	s.set[id] = struct{}{}
	s.order = append(s.order, id)
	if len(s.order) > s.max {
		delete(s.set, s.order[0])
		s.order = s.order[1:]
	}
	return true
}

// larkLogger sends the SDK's logs to slog, with the app secret removed.
type larkLogger struct {
	log    *slog.Logger
	redact []string
}

func (l larkLogger) emit(level slog.Level, args []any) {
	msg := strings.TrimSpace(fmt.Sprint(args...))
	for _, secret := range l.redact {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "***")
		}
	}
	l.log.Log(context.Background(), level, msg, "src", "lark-sdk")
}

func (l larkLogger) Debug(_ context.Context, a ...any) { l.emit(slog.LevelDebug, a) }
func (l larkLogger) Info(_ context.Context, a ...any)  { l.emit(slog.LevelInfo, a) }
func (l larkLogger) Warn(_ context.Context, a ...any)  { l.emit(slog.LevelWarn, a) }
func (l larkLogger) Error(_ context.Context, a ...any) { l.emit(slog.LevelError, a) }

var _ larkcore.Logger = larkLogger{}
