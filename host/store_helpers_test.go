package host_test

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/store"
)

// storeValue is a value that is both seams of a Store.
type storeValue interface {
	store.Records
	store.Liveness
}

// asStore makes a Store of one value that is both Records and Liveness.
func asStore(s storeValue) store.Store { return store.Store{Records: s, Liveness: s} }

// memStore is a Store of one new MemoryStore.
func memStore() store.Store { return asStore(store.NewMemoryStore()) }

// parts is a Store as one value with every method, for a test double that
// wraps a Store and overrides some. Its List passes through when the Records
// it holds list.
type parts struct {
	store.Records
	store.Liveness
}

// partsOf makes the parts of a Store.
func partsOf(s store.Store) parts { return parts{Records: s.Records, Liveness: s.Liveness} }

func (p parts) List(ctx context.Context, q store.ListQuery) (store.ListPage, error) {
	lister, ok := p.Records.(store.Lister)
	if !ok {
		return store.ListPage{}, errors.New("the Records do not list")
	}
	return lister.List(ctx, q)
}

// listAll returns every Record of a Store's listing, paging through it.
func listAll(ctx context.Context, st store.Store) ([]record.Info, error) {
	lister, ok := st.Records.(store.Lister)
	if !ok {
		return nil, errors.New("the Records do not list")
	}
	var all []record.Info
	var q store.ListQuery
	for {
		page, err := lister.List(ctx, q)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Infos...)
		if page.Next == "" {
			return all, nil
		}
		q.Cursor = page.Next
	}
}

// recordState is the part of a Header's State these tests read: Gotato's own
// bytes, which no store reads, so a test decodes them itself.
type recordState struct {
	Running gotato.RunID `json:"running"`
	Busy    bool         `json:"busy"`
}

// stateOf decodes the State of a Loaded Record's Header.
func stateOf(loaded record.Loaded) recordState {
	var state recordState
	if len(loaded.Header.State) > 0 {
		if err := json.Unmarshal(loaded.Header.State, &state); err != nil {
			panic(err)
		}
	}
	return state
}
