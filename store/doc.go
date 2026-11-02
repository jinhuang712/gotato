// Package store saves Records: the contract a store keeps (Store, Records,
// Liveness, Lister and Definitions), the Writer that binds a Record to a
// Records, loading (Open, View), and the memory and file stores that ship. A
// Record is package record, which does not know where it is saved; this
// package imports it, never the reverse.
//
// To write a Store adapter, read these first: Store (its doc lists the rules
// adapters miss), record.Entry, record.Loaded, record.Header, record.Info, and
// InstanceRow with InstanceState, plus the errors ErrExists, ErrConflict and
// ErrExpired here and record.ErrNotFound. Prove the adapter with
// store/storetest. MemoryStore and FileStore are the Stores that ship. Writer,
// WriteStats and the Writer options are for hosts that run Sessions on many
// Instances (the service package uses them); a program that runs one Session
// needs only a Record and a store to open it from.
package store
