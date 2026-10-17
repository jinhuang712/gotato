package session_test

import (
	"os"
	"testing"

	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit/storetest"
)

func TestMemoryStoreConformance(t *testing.T) {
	storetest.Run(t, func() (storetest.Open, func(), error) {
		store := session.NewMemoryStore()
		return func() session.Store { return store }, nil, nil
	})
}

// Every handle is a FileStore of its own on one directory, as two processes
// sharing it are.
func TestFileStoreConformance(t *testing.T) {
	storetest.Run(t, func() (storetest.Open, func(), error) {
		dir, err := os.MkdirTemp("", "gotato-filestore-*")
		if err != nil {
			return nil, nil, err
		}
		open := func() session.Store {
			store, err := session.NewFileStore(dir)
			if err != nil {
				panic(err)
			}
			return store
		}
		return open, func() { _ = os.RemoveAll(dir) }, nil
	})
}
