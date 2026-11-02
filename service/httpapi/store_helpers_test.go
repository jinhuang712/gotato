package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// storeValue is a value that is both seams of a Store.
type storeValue interface {
	session.Records
	session.Liveness
}

// asStore makes a Store of one value that is both Records and Liveness.
func asStore(s storeValue) session.Store { return session.Store{Records: s, Liveness: s} }

// memStore is a Store of one new MemoryStore.
func memStore() session.Store { return asStore(session.NewMemoryStore()) }

// parts is a Store as one value with every method, for a test double that
// wraps a Store and overrides some. Its List passes through when the Records
// it holds list.
type parts struct {
	session.Records
	session.Liveness
}

// partsOf makes the parts of a Store.
func partsOf(s session.Store) parts { return parts{Records: s.Records, Liveness: s.Liveness} }

func (p parts) List(ctx context.Context, q session.ListQuery) (session.ListPage, error) {
	lister, ok := p.Records.(session.Lister)
	if !ok {
		return session.ListPage{}, errors.New("the Records do not list")
	}
	return lister.List(ctx, q)
}

// recordState is the part of a Header's State these tests read: Gotato's own
// bytes, which no store reads, so a test decodes them itself.
type recordState struct {
	Running gotato.RunID `json:"running"`
	Busy    bool         `json:"busy"`
}

// stateOf decodes the State of a Loaded Record's Header.
func stateOf(loaded session.Loaded) recordState {
	var state recordState
	if len(loaded.Header.State) > 0 {
		if err := json.Unmarshal(loaded.Header.State, &state); err != nil {
			panic(err)
		}
	}
	return state
}
