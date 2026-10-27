// Package reftasks holds the tasks of the reference workload that
// plan/GOALS.md measures: each task has a history, prompts, Tools, and a fake
// Model that plays it the same way every time, with a check of its answers.
// It is a test tool of Gotato and lives under internal/; package
// internal/workload plays the tasks through a Runner.
package reftasks
