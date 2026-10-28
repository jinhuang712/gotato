package gotato

import (
	"context"
)

// syncLoadedTools sets the loaded Tools to the ones a rewritten Transcript
// lists, so the Session offers the Tools a load would: a compaction unloads
// the Tools its Segment and kept tail did not call. A Tool of a ToolSet the
// Agent installed active is never loaded or unloaded.
func (a *Session) syncLoadedTools() error {
	source, ok := a.transcript.(LoadedToolSource)
	if !ok {
		return nil
	}
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	return a.registry.syncLoaded(source.LoadedTools())
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
	source, _ := a.transcript.(LoadedToolSource)
	a.registryMu.Lock()
	report, err := a.registry.apply(results, source)
	a.registryMu.Unlock()
	if err != nil {
		return asRuntimeError(err)
	}
	for _, name := range report.unavailable {
		a.stage(ExtensionFailed{Header: header, Stage: "toolset:" + name, Code: CodeToolResolutionFailure})
	}
	for _, id := range report.gone {
		a.stage(ToolUnloaded{Header: header, ToolID: id, Why: ToolUnloadedWhyGone})
	}
	return nil
}

// refreshTools re-reads every ToolSource. Static Tools and ToolSets are
// untouched; only the dynamic part of the registry is rebuilt.
func (a *Session) refreshTools(ctx context.Context) error {
	if !a.registry.hasSources() {
		return nil
	}
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	return a.registry.refreshSources(ctx)
}

// refreshToolsInRun is refreshTools for a Run: a ToolSource that fails leaves
// the last set in place and stores an ExtensionFailed Event, as a failing
// ToolSet does. Only a ctx that ended fails the Run.
func (a *Session) refreshToolsInRun(ctx context.Context) *RuntimeError {
	err := a.refreshTools(ctx)
	if err == nil {
		return nil
	}
	if failure := contextFailure(ctx, "ToolSource"); failure != nil {
		return failure
	}
	a.stage(ExtensionFailed{Header: a.nowHeader(), Stage: "toolsource", Code: asRuntimeError(err).Code})
	return nil
}
