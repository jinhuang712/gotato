package store

import (
	"bytes"
	"context"
	"sync"

	"github.com/jinhuang712/gotato/record"
)

// MemoryDefinitions is a Definitions store that keeps its documents in
// memory. It is safe for concurrent use.
type MemoryDefinitions struct {
	mu   sync.Mutex
	docs map[[2]string][]byte
}

// NewMemoryDefinitions returns an empty MemoryDefinitions.
func NewMemoryDefinitions() *MemoryDefinitions {
	return &MemoryDefinitions{docs: map[[2]string][]byte{}}
}

// SaveDefinition stores a copy of doc under (kind, name).
func (m *MemoryDefinitions) SaveDefinition(_ context.Context, kind, name string, doc []byte) error {
	if err := CheckDefinitionKey(kind, name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docs[[2]string{kind, name}] = bytes.Clone(doc)
	return nil
}

// LoadDefinition returns a copy of the document under (kind, name), or
// ErrNotFound.
func (m *MemoryDefinitions) LoadDefinition(_ context.Context, kind, name string) ([]byte, error) {
	if err := CheckDefinitionKey(kind, name); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, ok := m.docs[[2]string{kind, name}]
	if !ok {
		return nil, record.ErrNotFound
	}
	return bytes.Clone(doc), nil
}
