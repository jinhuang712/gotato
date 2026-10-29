package gotato

import (
	"context"
)

// syncLoadedTools sets the loaded Tools to the ones a rewritten Transcript
// lists, so the Session offers the Tools a load would: a compaction unloads
// the Tools its Segment and kept tail did not call. A Tool of a ToolSet the
// Agent installed active is never loaded or unloaded.
func (a *Session) syncLoadedTools() {
	source, ok := a.transcript.(LoadedToolSource)
	if !ok {
		return
	}
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	a.registry.syncLoaded(source.LoadedTools())
}

// refreshToolSets resolves the ToolSets again at the start of a Run (the first
// Run uses NewSession's resolution), so a change on a ToolSet's side takes
// effect at this Run and no later than the next. A ToolSet that fails is
// skipped with an ExtensionFailed Event; a loaded Tool its ToolSet no longer
// carries is unloaded with a stored tool_unloaded Event (why: gone), which the
// Record folds, so every instance that loads the Record offers the same Tools.
func (a *Session) refreshToolSets(ctx context.Context, header Header) *RuntimeError {
	if len(a.registry.sets) == 0 {
		return nil
	}
	results, err := a.registry.resolveAll(ctx)
	if err != nil {
		return asRuntimeError(err)
	}
	kept, err := a.registry.screen(a.registry.catalogIDs(results))
	if err != nil {
		return asRuntimeError(err)
	}
	source, _ := a.transcript.(LoadedToolSource)
	a.registryMu.Lock()
	report := a.registry.apply(results, kept, source)
	a.registryMu.Unlock()
	for _, name := range report.unavailable {
		a.stage(ExtensionFailed{Header: header, Stage: "toolset:" + name, Code: CodeToolResolutionFailure})
	}
	for _, id := range report.gone {
		a.stage(ToolUnloaded{Header: header, ToolID: id, Why: ToolUnloadedWhyGone})
	}
	return nil
}

// refreshTools re-reads every ToolSource and asks the filter again, with no
// lock held, then swaps both in. Static Tools and ToolSets are untouched; only
// the dynamic part of the registry is rebuilt. sourceErr is a failure of the
// ToolSources, which leaves the last set in place; filterErr is a failure of
// the filter.
func (a *Session) refreshTools(ctx context.Context) (sourceErr, filterErr error) {
	if !a.registry.hasSources() {
		return nil, nil
	}
	entries, err := a.registry.collectSources(ctx)
	if err != nil {
		return err, nil
	}
	kept, err := a.registry.screen(a.registry.catalogIDs(nil))
	if err != nil {
		return nil, err
	}
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	a.registry.refreshSources(entries, kept)
	return nil, nil
}

// refreshToolsInRun is refreshTools for a Run: a ToolSource that fails leaves
// the last set in place and stores an ExtensionFailed Event, as a failing
// ToolSet does. Only a ctx that ended, or a filter that failed, fails the Run.
func (a *Session) refreshToolsInRun(ctx context.Context) *RuntimeError {
	sourceErr, filterErr := a.refreshTools(ctx)
	if filterErr != nil {
		return asRuntimeError(filterErr)
	}
	if sourceErr == nil {
		return nil
	}
	if failure := contextFailure(ctx, "ToolSource"); failure != nil {
		return failure
	}
	a.stage(ExtensionFailed{Header: a.nowHeader(), Stage: "toolsource", Code: asRuntimeError(sourceErr).Code})
	return nil
}
