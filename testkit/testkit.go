// Package testkit provides deterministic doubles for testing Gotato runtimes
// without a network provider: scripted and replayed Models, a recording
// FakeTool, an EventRecorder over a Subscription, Session fixtures, the echo/demo
// Models used by the CLI and the reference service.
//
// # Which fake do I use?
//
//	You want                                          Use
//	a model that answers a fixed sequence of replies  NewFakeModel(Text("a"), Text("b"))
//	a model that calls a tool, then answers           NewFakeModel(ToolCalls(call), Text("done"))
//	the same, and fail if it is called once too often NewReplayModel(...)
//	a model that decides from the request             NewFuncModel (with ToolResults, UserText)
//	a model that streams slowly, to cut it mid-way    a FakeModel with Delay set
//	a model that waits until the test lets it go      a FakeModel with Block set
//	a model that fails                                a FakeModel with Err set
//	a model that echoes the prompt (the CLI default)  EchoModel
//	a model that calls DemoEchoTool on "use-tool"     DemoModel
//	a tool that returns a fixed result                NewFakeTool
//	every Event of a Session, in order                RecordEvents
//
// A Script is the ModelEvents one model call streams. Text and ToolCalls
// build the two common ones; a tool call is
// gotato.ToolCall{ID: "c1", ToolID: "demo.echo", Arguments: []byte(`{"value":"x"}`)}.
// A FakeModel repeats its last Script once the list ends, so a loop always
// finishes; a ReplayModel fails instead.
package testkit
