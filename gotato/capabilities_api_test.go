package gotato_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// declaredTranscript is a Transcript whose declaration a test chooses.
type declaredTranscript struct {
	messages []gotato.Message
	caps     gotato.Capabilities
}

func (t *declaredTranscript) Len() int                          { return len(t.messages) }
func (t *declaredTranscript) Messages() []gotato.Message        { return t.messages }
func (t *declaredTranscript) Capabilities() gotato.Capabilities { return t.caps }
func (t *declaredTranscript) Append(m gotato.Message) error {
	t.messages = append(t.messages, m)
	return nil
}

// A wrapper over a Transcript that is not a Journal in its own right.
type hidingWrapper struct{ gotato.Transcript }

// A Transcript that declares Durable must declare the Journal, the Events and the
// Loaded Tools a store needs; one that does not is refused at NewSession, naming
// what it lacks, not found out weeks later as a Session that stores less.
func TestNewSessionRefusesDurableWithoutJournalEventsAndLoaded(t *testing.T) {
	record := session.New()
	cases := []struct {
		name    string
		caps    gotato.Capabilities
		missing string
	}{
		{"nothing", gotato.Capabilities{Durable: true}, "Journal, Events, Loaded"},
		{"no Journal", gotato.Capabilities{Durable: true, Events: record, Loaded: record}, "Journal"},
		{"no Events", gotato.Capabilities{Durable: true, Journal: record, Loaded: record}, "Events"},
		{"no Loaded", gotato.Capabilities{Durable: true, Journal: record, Events: record}, "Loaded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}), gotato.WithTranscript(&declaredTranscript{caps: c.caps}))
			if !gotato.IsCode(err, gotato.CodeInvalidArgument) || !strings.Contains(err.Error(), "without "+c.missing) {
				t.Fatalf("err = %v, want invalid_argument naming %q", err, c.missing)
			}
		})
	}
}

// A wrapper that hides the Journal of a durable Transcript while it still says
// Durable (it forwards the declaration of the Transcript it wraps, as one that
// embeds the interface does, but changes it) is refused.
func TestNewSessionRefusesAWrapperThatHidesTheJournalWhileDurable(t *testing.T) {
	inner := &declaredTranscript{caps: gotato.Capabilities{Durable: true}}
	_, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}), gotato.WithTranscript(hidingWrapper{inner}))
	if !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}

	// A wrapper that embeds the concrete type forwards every part, and is accepted.
	forwarding := struct{ *session.Record }{session.New()}
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}), gotato.WithTranscript(forwarding))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close(context.Background())
}

// A Transcript that declares nothing is a Session that stores nothing, and says
// so: it is accepted, and its Events are numbered in memory.
func TestNewSessionAcceptsATranscriptThatStoresNothing(t *testing.T) {
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}), gotato.WithTranscript(&declaredTranscript{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	if _, err := s.Prompt(context.Background(), gotato.UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
}

// A Transcript's declared ID is the Session's.
func TestSessionTakesTheDeclaredID(t *testing.T) {
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}), gotato.WithTranscript(&declaredTranscript{caps: gotato.Capabilities{ID: "declared-id"}}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	if s.ID() != "declared-id" {
		t.Fatalf("ID = %q", s.ID())
	}
}

// A single-valued option given twice is refused, naming it, and builds nothing:
// the later one would otherwise replace the earlier one in silence.
func TestNewSessionRefusesASingleValuedOptionGivenTwice(t *testing.T) {
	cases := []struct {
		name   string
		option func() gotato.SessionOption
	}{
		{"WithModel", func() gotato.SessionOption { return gotato.WithModel(testkit.EchoModel{}) }},
		{"WithInstruction", func() gotato.SessionOption { return gotato.WithInstruction("x") }},
		{"WithSampling", func() gotato.SessionOption { return gotato.WithSampling(gotato.Sampling{}) }},
		{"WithCacheKey", func() gotato.SessionOption { return gotato.WithCacheKey("k") }},
		{"WithMaxEffect", func() gotato.SessionOption { return gotato.WithMaxEffect(gotato.ToolEffectRead) }},
		{"WithLimits", func() gotato.SessionOption { return gotato.WithLimits(gotato.CoreLimits{}) }},
		{"WithContextBuilder", func() gotato.SessionOption { return gotato.WithContextBuilder(gotato.FullHistoryContext()) }},
		{"WithToolFilter", func() gotato.SessionOption { return gotato.WithToolFilter(func(string) bool { return true }) }},
		{"WithTranscript", func() gotato.SessionOption { return gotato.WithTranscript(&declaredTranscript{}) }},
		{"WithFeed", func() gotato.SessionOption { return withFeed(newFeed(nil)) }},
		{"WithRequestID", func() gotato.SessionOption { return withRequestID("r") }},
		{"WithRunStart", func() gotato.SessionOption { return withRunStart(func(gotato.RunID) {}) }},
		{"WithQueueHandoff", func() gotato.SessionOption { return withQueueHandoff() }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			options := []gotato.SessionOption{c.option(), c.option()}
			if c.name != "WithModel" {
				options = append(options, gotato.WithModel(testkit.EchoModel{}))
			}
			s, err := gotato.NewSession(context.Background(), options...)
			if err == nil {
				_ = s.Close(context.Background())
				t.Fatal("NewSession built a Session with the option given twice")
			}
			if !gotato.IsCode(err, gotato.CodeInvalidArgument) || !strings.Contains(err.Error(), c.name+" was given twice") {
				t.Fatalf("err = %v, want invalid_argument naming %s", err, c.name)
			}
		})
	}
}

// The options that add, rather than set, may be given again.
func TestNewSessionTakesTheOptionsThatAddMoreThanOnce(t *testing.T) {
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}),
		gotato.WithTools(), gotato.WithTools(), gotato.WithExtension(sampleStopper{}), gotato.WithExtension(sampleStopper{}))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close(context.Background())
}

type sampleStopper struct{}

func (sampleStopper) Stop(context.Context, gotato.TurnSnapshot) (gotato.StopDecision, error) {
	return gotato.StopDecision{}, nil
}
