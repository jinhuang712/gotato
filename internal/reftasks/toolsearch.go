package reftasks

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/jinhuang712/gotato/testkit"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// The tool_search task (GOALS.md G-G07 and G-G09 on a Session whose Tools load
// and unload): the Agent offers one base Tool and three ToolSets, a dozen
// catalog Tools in all. The Model finds them with search_tools.
const (
	// ToolSearchName is the task's name in reports.
	ToolSearchName = "tool_search"
	// ShopSetName and CarrierSetName are the task's listed ToolSets.
	ShopSetName    = "shop"
	CarrierSetName = "carrier"
	// WarehouseSetName is the task's folded ToolSet.
	WarehouseSetName = "warehouse"
)

// toolSearchInstruction is the instruction of the tool_search task.
const toolSearchInstruction = "You answer questions about orders, shipping and stock. Use orders.lookup to find an order's status. " +
	"Other tools are in the catalog of search_tools: load the ones you need, then call them. Answer in one short sentence."

// toolSearchTool is one catalog Tool of the task: its local ID, its
// description, its Effect and the string arguments it reads.
type toolSearchTool struct {
	id, description string
	effect          gotato.ToolEffect
	args            []string
}

var toolSearchTools = map[string][]toolSearchTool{
	ShopSetName: {
		{"invoice", "Issues the invoice of an order and returns its number.", gotato.ToolEffectRead, []string{"order"}},
		{"cancel", "Cancels an order that has not shipped yet.", gotato.ToolEffectWriteExternal, []string{"order"}},
		{"history", "Lists the past orders of a customer by the customer ID.", gotato.ToolEffectRead, []string{"customer"}},
		{"address", "Changes the delivery address of an order before it ships.", gotato.ToolEffectWriteExternal, []string{"order", "address"}},
	},
	CarrierSetName: {
		{"quote", "Quotes the price and transit time of shipping a parcel to a city.", gotato.ToolEffectRead, []string{"city", "kg"}},
		{"track", "Follows a parcel by its tracking number and returns its last scan.", gotato.ToolEffectRead, []string{"tracking"}},
		{"label", "Prints the label to stick on the box of an order.", gotato.ToolEffectWriteLocal, []string{"order"}},
	},
	WarehouseSetName: {
		{"stock", "Counts the units of an item on the shelves.", gotato.ToolEffectRead, []string{"sku"}},
		{"reserve", "Holds units of an item for an order so nobody else can buy them.", gotato.ToolEffectWriteLocal, []string{"sku", "units"}},
		{"restock", "Orders more units of an item from its supplier.", gotato.ToolEffectWriteExternal, []string{"sku", "units"}},
		{"locations", "Lists the shelves where an item is stored.", gotato.ToolEffectRead, []string{"sku"}},
		{"audit", "Starts a count of one shelf and reports the differences.", gotato.ToolEffectRead, []string{"shelf"}},
	},
}

var toolSearchSetDescriptions = map[string]string{
	ShopSetName:      "Orders of the shop: invoices, cancellations and addresses.",
	CarrierSetName:   "The parcel carrier: prices, tracking and labels.",
	WarehouseSetName: "The tea warehouse: stock, reservations and shelves.",
}

// toolSearchSet is a ToolSet of fixed Tools. It keeps no state.
type toolSearchSet struct {
	name  string
	tools []gotato.Tool
}

func (s toolSearchSet) Spec() gotato.ToolSetSpec {
	return gotato.ToolSetSpec{Name: s.name, Description: toolSearchSetDescriptions[s.name]}
}

func (s toolSearchSet) Tools(context.Context) ([]gotato.Tool, error) { return s.tools, nil }

// newToolSearchSet builds one ToolSet of the task. Each Tool returns its
// string arguments with a fixed fact of its own, so a result is the same on
// every play.
func newToolSearchSet(name string) toolSearchSet {
	set := toolSearchSet{name: name}
	for _, def := range toolSearchTools[name] {
		props := map[string]any{}
		for _, arg := range def.args {
			props[arg] = map[string]any{"type": "string"}
		}
		schema, _ := json.Marshal(map[string]any{
			"type": "object", "additionalProperties": false, "required": def.args, "properties": props,
		})
		tool := toolSearchFunc{
			spec: gotato.ToolSpec{ID: def.id, Description: def.description, InputSchema: schema},
			fact: name + "." + def.id + " done",
		}
		set.tools = append(set.tools, gotato.ToolWithEffect(tool, def.effect))
	}
	return set
}

// toolSearchFunc is a Tool that echoes its arguments with a fixed fact.
type toolSearchFunc struct {
	spec gotato.ToolSpec
	fact string
}

func (t toolSearchFunc) Spec() gotato.ToolSpec { return t.spec }

func (t toolSearchFunc) Execute(_ context.Context, use gotato.ToolUse, _ gotato.ToolProgressFunc) (gotato.ToolResult, error) {
	var in map[string]string
	if err := json.Unmarshal(use.Arguments, &in); err != nil {
		return gotato.ToolResult{}, err
	}
	out := map[string]string{"result": t.fact}
	maps.Copy(out, in)
	if t.spec.ID == "invoice" {
		out["number"] = "INV-" + in["order"]
	}
	encoded, _ := json.Marshal(out)
	return gotato.ToolResult{Status: gotato.ToolResultStatusOK, Content: []gotato.ContentPart{{Kind: gotato.ContentKindJSON, Text: string(encoded)}}}, nil
}

// ToolSearchOptions returns the Options the tool_search task adds to its Agent:
// the shop and the carrier as listed ToolSets, the warehouse as a folded one.
func ToolSearchOptions() []gotato.SessionOption {
	return []gotato.SessionOption{
		gotato.WithToolSet(newToolSearchSet(ShopSetName)),
		gotato.WithToolSet(newToolSearchSet(CarrierSetName)),
		gotato.WithFoldedToolSet(newToolSearchSet(WarehouseSetName)),
	}
}

// How the Model reaches a Tool it does not see yet.
const (
	reachBase     = "base"     // visible always
	reachName     = "name"     // search_tools by name
	reachQuery    = "query"    // search_tools by a query
	reachExpand   = "expand"   // list the folded ToolSet, then load by name
	reachCareless = "careless" // call it unloaded, read "loaded now", call again
)

type toolSearchNeed struct {
	tool, args, reach, query string
}

type toolSearchPlan struct {
	needs  []toolSearchNeed
	answer string
	// keyword is what the Run's answer must hold.
	keyword string
}

// The prompts of the task, in order.
var toolSearchPrompts = []string{
	"Where is order 42, and can I have its invoice?",
	"What would shipping order 42 to Lyon cost?",
	"What does the warehouse offer, and is TEA-1 in stock?",
	"Please hold two TEA-1 for order 42.",
	"Please change the address of order 42 to 5 Rue Lenoir.",
	"Where is the parcel of order 42? Its tracking number is TRK-9.",
	"Thank you.",
}

// toolSearchPlans is what the Model does for each prompt.
var toolSearchPlans = map[string]toolSearchPlan{
	toolSearchPrompts[0]: {keyword: "INV-42", needs: []toolSearchNeed{
		{tool: ordersLookupID, args: `{"id":"42"}`, reach: reachBase},
		{tool: "shop.invoice", args: `{"order":"42"}`, reach: reachName},
	}},
	toolSearchPrompts[1]: {keyword: "Lyon", needs: []toolSearchNeed{
		{tool: "carrier.quote", args: `{"city":"Lyon","kg":"1"}`, reach: reachQuery, query: "shipping parcel"},
	}},
	toolSearchPrompts[2]: {keyword: "TEA-1", needs: []toolSearchNeed{
		{tool: "warehouse.stock", args: `{"sku":"TEA-1"}`, reach: reachExpand},
	}},
	toolSearchPrompts[3]: {keyword: "units=2", needs: []toolSearchNeed{
		{tool: "warehouse.reserve", args: `{"sku":"TEA-1","units":"2"}`, reach: reachCareless},
	}},
	toolSearchPrompts[4]: {keyword: "Lenoir", needs: []toolSearchNeed{
		{tool: "shop.address", args: `{"order":"42","address":"5 Rue Lenoir"}`, reach: reachName},
	}},
	toolSearchPrompts[5]: {keyword: "TRK-9", needs: []toolSearchNeed{
		{tool: "carrier.track", args: `{"tracking":"TRK-9"}`, reach: reachName},
	}},
	toolSearchPrompts[6]: {keyword: "welcome", answer: "You are welcome."},
}

// ToolSearchTask is the tool_search task: seven prompts on one Session, in
// which the Model loads Tools by name, by a query, and by listing a folded
// ToolSet, calls a Tool it did not load and calls it again once it is loaded,
// and uses a Tool it loaded in an earlier Run. A Session that compacts between
// two Runs unloads some of them, and the Model loads them again when it needs
// them: it reads from the request which Tools it has.
func ToolSearchTask() Task {
	return Task{
		Name:        ToolSearchName,
		Instruction: toolSearchInstruction,
		Prompts:     toolSearchPrompts,
		Tools:       []gotato.Tool{OrdersLookup()},
		Options:     ToolSearchOptions(),
		Model:       toolSearchModel{},
		Check: func(answers []string) error {
			if len(answers) != len(toolSearchPrompts) {
				return fmt.Errorf("got %d answers, want %d", len(answers), len(toolSearchPrompts))
			}
			for i, prompt := range toolSearchPrompts {
				if want := toolSearchPlans[prompt].keyword; !strings.Contains(answers[i], want) {
					return fmt.Errorf("answer %d = %q, want it to hold %q", i+1, answers[i], want)
				}
			}
			return nil
		},
	}
}

// toolSearchModel plays the task from the request alone: the Tools it sees
// say what is loaded, the Messages of the Run say what it has done.
type toolSearchModel struct{}

// Stream implements gotato.Model.
func (toolSearchModel) Stream(_ context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	return playScript(playToolSearch(request)), nil
}

func playToolSearch(request gotato.ModelRequest) testkit.Script {
	prompt, _ := lastPrompt(request.Messages)
	plan, ok := toolSearchPlans[prompt]
	if !ok {
		return testkit.Text("Noted.")
	}
	// What the Run has done so far: the calls after the last prompt, with
	// the results they got.
	var run []gotato.Message
	for i, v := range slices.Backward(request.Messages) {
		if v.Role == gotato.RoleUser {
			run = request.Messages[i+1:]
			break
		}
	}
	calls := toolCallsByID(run)
	results := map[gotato.ToolCallID]gotato.ToolResult{}
	for _, message := range run {
		if message.ToolResult != nil {
			results[message.ToolResult.CallID] = *message.ToolResult
		}
	}
	succeeded := func(tool string) (string, bool) {
		for id, call := range calls {
			if result, ok := results[id]; ok && call.ToolID == tool && result.Status == gotato.ToolResultStatusOK {
				return resultText(result), true
			}
		}
		return "", false
	}
	searched := func(key, value string) bool {
		for _, call := range calls {
			if call.ToolID != gotato.SearchToolName {
				continue
			}
			var in map[string]json.RawMessage
			if json.Unmarshal(call.Arguments, &in) == nil && strings.Contains(string(in[key]), value) {
				return true
			}
		}
		return false
	}
	tried := func(tool string) bool {
		for _, call := range calls {
			if call.ToolID == tool {
				return true
			}
		}
		return false
	}
	visible := map[string]bool{}
	for _, spec := range request.Tools {
		visible[spec.ID] = true
	}
	n := len(request.Messages) + 1
	call := func(tool, arguments string) testkit.Script {
		return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID("call-" + strconv.Itoa(n)), ToolID: tool, Arguments: []byte(arguments)})
	}
	search := func(key string, value any) testkit.Script {
		encoded, _ := json.Marshal(map[string]any{key: value})
		return call(gotato.SearchToolName, string(encoded))
	}

	var facts []string
	for _, need := range plan.needs {
		if text, done := succeeded(need.tool); done {
			facts = append(facts, summarize(text))
			continue
		}
		if visible[need.tool] {
			return call(need.tool, need.args)
		}
		set, _, _ := strings.Cut(need.tool, ".")
		switch {
		case need.reach == reachCareless && !tried(need.tool):
			return call(need.tool, need.args)
		case need.reach == reachQuery && !searched("queries", need.query):
			return search("queries", []string{need.query})
		case need.reach == reachExpand && !searched("toolset", set):
			return search("toolset", set)
		}
		return search("names", []string{need.tool})
	}
	if plan.answer != "" {
		return testkit.Text(plan.answer)
	}
	return testkit.Text("Done: " + strings.Join(facts, "; ") + ".")
}

// summarize turns a JSON result of string fields into "key=value" pairs in key
// order.
func summarize(text string) string {
	var fields map[string]string
	if err := json.Unmarshal([]byte(text), &fields); err != nil {
		return text
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, key := range keys {
		pairs[i] = key + "=" + fields[key]
	}
	return strings.Join(pairs, " ")
}

// WithLoadedToolsOf is a session.Option for a Record made from messages alone:
// it sets as loaded the catalog Tools the Tool Results among them list, in
// order, which is what a Session that played them holds as long as it did not
// compact. A Record built with session.WithMessages starts with no loaded
// Tools, so a check that copies a Session's history to a new Record adds this.
func WithLoadedToolsOf(messages []gotato.Message) session.RecordOption {
	var ids []string
	for _, message := range messages {
		if message.ToolResult == nil {
			continue
		}
		for _, id := range message.ToolResult.LoadedTools {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return func(r *session.Record) { r.SetLoadedTools(ids) }
}
