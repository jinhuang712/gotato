package mcp

// The names below left the public API; the external tests reach them here.

var (
	ConvertResult = convertResult
	EffectOf      = effectOf
	SlimSchema    = slimSchema
	FirstText     = firstText
)

// ListGen is the count of changes that make a kept tool list stale.
func (s *ToolSet) ListGen() uint64 { return s.gen.Load() }
