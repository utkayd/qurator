package storetest

import (
	"testing"

	"github.com/utkayd/qurator/internal/store"
)

// TestMemStore proves the contract suite is satisfiable before any real driver exists.
func TestMemStore(t *testing.T) {
	RunStoreContract(t, func(t *testing.T) store.Store { return NewMemStore() })
}
