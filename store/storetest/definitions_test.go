package storetest_test

import (
	"testing"

	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/store/storetest"
)

func TestMemoryDefinitions(t *testing.T) {
	storetest.RunDefinitions(t, func() (storetest.OpenDefinitions, func(), error) {
		shared := store.NewMemoryDefinitions()
		return func() store.Definitions { return shared }, nil, nil
	})
}

func TestFileDefinitions(t *testing.T) {
	storetest.RunDefinitions(t, func() (storetest.OpenDefinitions, func(), error) {
		dir := t.TempDir()
		return func() store.Definitions {
			d, err := store.NewFileDefinitions(dir)
			if err != nil {
				t.Fatal(err)
			}
			return d
		}, nil, nil
	})
}
