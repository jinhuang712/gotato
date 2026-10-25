package bash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/metrics"
	"strings"
	"sync/atomic"
	"time"

	"github.com/itchyny/gojq"
)

const (
	// MaxJQInputBytes is the most bytes of input jq holds in memory. A JSON
	// value takes many times its text once it is parsed, so the bound is lower
	// than MaxBufferBytes.
	MaxJQInputBytes = 32 << 20
	// MaxJQHeapGrowth is how much the heap of the process may grow while one
	// jq program runs before the program is stopped. It is a safety net for a
	// program that builds a value without end, such as [range(1e9)].
	MaxJQHeapGrowth = 512 << 20
	// MaxJQRuntime is how long one jq program may run when ctx has no earlier
	// deadline. A program that does not end (`repeat(1)|empty`) stops here.
	MaxJQRuntime = 30 * time.Second
)

// The limits of a program, as variables so that a test can lower them.
var (
	jqHeapLimit    uint64 = MaxJQHeapGrowth
	jqRuntimeLimit        = MaxJQRuntime
)

const jqUsage = `Usage: jq [OPTIONS] FILTER [FILE...]
Command-line JSON processor, jq 1.7 compatible, for the files of the result
directory and for standard input. With no FILE, or when FILE is -, read
standard input. With no FILTER the filter is "." (copy the input).

Supported options:
  -r, --raw-output      output strings without escapes and quotes
  -j, --join-output     like -r, and print no newline after each output
  -c, --compact-output  compact instead of pretty-printed output
  -s, --slurp           read all inputs into one array
  -n, --null-input      use null as the only input; read with input and inputs
  -R, --raw-input       read each line as a string instead of JSON
  -e, --exit-status     exit 1 if the last output is false or null, 4 if none
  -S, --sort-keys       sort the keys of every object on output
  -a, --ascii-output    write non-ASCII characters as \uXXXX
  -M, --monochrome-output  accepted; output is never colored
      --tab             indent with a tab
      --indent N        indent with N spaces (0 to 7; 0 is like -c)
      --arg NAME TEXT   set $NAME to the string TEXT
      --argjson NAME JSON  set $NAME to the JSON value
      --help            display this help and exit

Exit status: 0 ok, 2 usage or input error, 3 filter does not compile, 5 error
while running the filter; with -e, 1 or 4 as above.

The filter language is that of jq 1.7, run by gojq. What is not supported is an
error and never a different answer: modules (import, include), now, localtime
and strflocaltime (they depend on the clock or the machine), the environment
($ENV and env are empty), and options other than the ones above.

Known differences:
  - The order of the keys of an object. jq keeps the order of the input; gojq
    keeps no order, so this engine writes an object in the order of the input
    only when it reaches the output unchanged (.  .items[0]  .[]). An object the
    filter builds or changes (an object literal, del, +, with_entries) has its
    keys sorted. Everything that walks the keys sees them sorted: .[] on an
    object, to_entries, with_entries, paths, tostream, map_values, tojson,
    tostring and @json. keys_unsorted is not defined.
  - Numbers. An integer is exact at any size, where jq loses the digits of a
    large one once it calculates with it. A number that is not an integer is
    written as its value: 1.10 is 1.1, 3.0 is 3, 1e3 is 1000, and 1e1000 is
    1.7976931348623157e+308. Inside a string (tojson, tostring, @text, @csv and
    interpolation) a number is written by gojq: 1.5e-7 for 1.5e-07, and
    100000000000000000 for 1e+17.
  - Regular expressions are those of RE2: no back-references or look-around.
  - $__loc__ and input_line_number are not defined.
  - Error messages, and the messages of a filter that does not compile, are
    gojq's words; the exit statuses are jq's. A parse error of the input is
    worded as jq words it, but its position can differ.
  - Input is held in memory, up to 32 MiB, and a filter is stopped after 30
    seconds or when it has grown the heap by 512 MiB.
`

type jqOptions struct {
	raw, join, compact, slurp, null, rawInput, exitStatus, sortKeys, ascii bool
	indent                                                                 string
	filter                                                                 string
	haveFilter                                                             bool
	files                                                                  []string
	argNames                                                               []string
	argValues                                                              []any
}

var jqSupported = "-r, -j, -c, -s, -n, -R, -e, -S, -a, -M, --tab, --indent, --arg, --argjson, --help"

// jqUsageError writes an error about the options in the words of jq.
func jqUsageError(c *Call, msg string) int {
	_, _ = fmt.Fprintf(c.Stderr, "jq: %s\nUse jq --help for help with command-line options,\nor see the jq manpage, or online docs  at https://jqlang.github.io/jq\n", msg)
	return 2
}

// jqUnsupported writes the error for an option that jq has and this one does
// not, with the list of the ones it has.
func jqUnsupported(c *Call, option string) int {
	_, _ = fmt.Fprintf(c.Stderr, "jq: option '%s' is not supported. Supported options: %s. Try 'jq --help'\n", option, jqSupported)
	return 2
}

func parseJQArgs(c *Call, orders keyOrders) (*jqOptions, int) {
	o := &jqOptions{indent: "  "}
	args := c.Args
	positional := func(a string) {
		if !o.haveFilter {
			o.filter, o.haveFilter = a, true
		} else {
			o.files = append(o.files, a)
		}
	}
	named := map[string]any{}
	var namedOrder []string
	addVar := func(name string, v any) {
		for i, n := range o.argNames {
			if n == "$"+name {
				o.argValues[i] = v
				named[name] = v
				return
			}
		}
		o.argNames = append(o.argNames, "$"+name)
		o.argValues = append(o.argValues, v)
		named[name] = v
		namedOrder = append(namedOrder, name)
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			for _, rest := range args[i+1:] {
				positional(rest)
			}
			i = len(args)
		case strings.HasPrefix(a, "--"):
			switch a[2:] {
			case "raw-output":
				o.raw = true
			case "join-output":
				o.raw, o.join = true, true
			case "compact-output":
				o.compact = true
			case "slurp":
				o.slurp = true
			case "null-input":
				o.null = true
			case "raw-input":
				o.rawInput = true
			case "exit-status":
				o.exitStatus = true
			case "sort-keys":
				o.sortKeys = true
			case "ascii-output":
				o.ascii = true
			case "monochrome-output":
			case "tab":
				o.indent = "\t"
			case "indent":
				if i+1 >= len(args) {
					return nil, jqUsageError(c, "--indent takes a number between -1 and 7")
				}
				i++
				n := args[i]
				if len(n) != 1 || n[0] < '0' || n[0] > '7' {
					return nil, jqUsageError(c, "--indent takes a number between -1 and 7")
				}
				o.indent = strings.Repeat(" ", int(n[0]-'0'))
			case "arg":
				if i+2 >= len(args) {
					return nil, jqUsageError(c, "--arg takes two parameters (e.g. --arg varname value)")
				}
				addVar(args[i+1], args[i+2])
				i += 2
			case "argjson":
				if i+2 >= len(args) {
					return nil, jqUsageError(c, "--argjson takes two parameters (e.g. --argjson varname text)")
				}
				p := &jsonParser{data: []byte(args[i+2]), orders: orders}
				v, ok, err := p.next()
				if err == nil && ok {
					p.skipSpace()
					if p.pos < len(p.data) {
						ok = false
					}
				}
				if err != nil || !ok {
					return nil, jqUsageError(c, "invalid JSON text passed to --argjson")
				}
				addVar(args[i+1], v)
				i += 2
			case "help":
				return nil, -1
			default:
				return nil, jqUnsupported(c, a)
			}
		case len(a) > 1 && a[0] == '-':
			for _, ch := range a[1:] {
				switch ch {
				case 'r':
					o.raw = true
				case 'j':
					o.raw, o.join = true, true
				case 'c':
					o.compact = true
				case 's':
					o.slurp = true
				case 'n':
					o.null = true
				case 'R':
					o.rawInput = true
				case 'e':
					o.exitStatus = true
				case 'S':
					o.sortKeys = true
				case 'a':
					o.ascii = true
				case 'M':
				case 'h':
					return nil, -1
				default:
					return nil, jqUnsupported(c, "-"+string(ch))
				}
			}
		default:
			positional(a)
		}
	}
	if !o.haveFilter {
		o.filter = "."
	}
	if o.compact {
		o.indent = ""
	}
	o.argNames = append(o.argNames, "$ARGS")
	argsObj := map[string]any{"positional": []any{}, "named": named}
	orders.record(argsObj, []string{"positional", "named"})
	orders.record(named, namedOrder)
	o.argValues = append(o.argValues, argsObj)
	return o, 0
}

// jqInput is the inputs of one run: the values of the files, one after
// another, or their lines, or all of them as one.
type jqInput struct {
	c       *Call
	opts    *jqOptions
	orders  keyOrders
	names   []string
	next    int // the next file to open
	cur     *jsonParser
	curName string
	lines   *bytes.Reader
	data    []byte // the current file, for line numbers
	line    int
	started bool
	done    bool
	slurped bool
	// failed is set when a file could not be read or parsed, which makes the
	// exit status 2.
	failed bool
}

// position is the place jq names in an error: the file and the line count at
// the last value read, or <unknown> before any input was read.
func (in *jqInput) position() string {
	if !in.started {
		return "<unknown>"
	}
	return fmt.Sprintf("%s:%d", in.curName, in.line)
}

func (in *jqInput) filename() any {
	if !in.started {
		return nil
	}
	return in.curName
}

func (in *jqInput) readFile(name string) ([]byte, bool) {
	r, err := in.c.open(name)
	if err != nil {
		fmt.Fprintf(in.c.Stderr, "jq: error: Could not open file %s: %s\n", name, errText(err))
		in.failed = true
		return nil, false
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(io.LimitReader(r, MaxJQInputBytes+1))
	if err == nil && len(data) > MaxJQInputBytes {
		err = fmt.Errorf("input too large to hold in memory (the most is %d bytes)", MaxJQInputBytes)
	}
	if err != nil {
		fmt.Fprintf(in.c.Stderr, "jq: error: Could not read %s: %s\n", name, errText(err))
		in.failed = true
		return nil, false
	}
	return data, true
}

func (in *jqInput) fileNames() []string {
	if len(in.opts.files) == 0 {
		return []string{"-"}
	}
	return in.opts.files
}

// Next is the one value of the next input, or false when there is none. An
// error value is a parse error; the reading stops after it.
func (in *jqInput) Next() (any, bool) {
	if in.done {
		return nil, false
	}
	if in.names == nil {
		in.names = in.fileNames()
	}
	if in.opts.slurp {
		if in.slurped {
			return nil, false
		}
		in.slurped = true
		in.done = true
		return in.slurp()
	}
	for {
		if in.cur == nil && in.lines == nil {
			if in.next >= len(in.names) {
				in.done = true
				return nil, false
			}
			name := in.names[in.next]
			in.next++
			data, ok := in.readFile(name)
			if !ok {
				continue
			}
			in.begin(name, data)
		}
		if in.opts.rawInput {
			if in.lines.Len() == 0 {
				in.lines = nil
				continue
			}
			rest := in.data[len(in.data)-in.lines.Len():]
			n := bytes.IndexByte(rest, '\n')
			var line []byte
			if n < 0 {
				line, n = rest, len(rest)-1
			} else {
				line = rest[:n]
			}
			_, _ = in.lines.Seek(int64(n+1), io.SeekCurrent)
			if n < len(rest)-1 || rest[len(rest)-1] == '\n' {
				in.line++
			}
			return validUTF8(string(line)), true
		}
		v, ok, err := in.cur.next()
		if err != nil {
			in.line = in.countLines(in.cur.pos)
			in.done = true
			return &jqParseError{err: err, pos: in.position()}, true
		}
		if !ok {
			in.cur = nil
			continue
		}
		in.line = in.countLines(in.cur.pos)
		return v, true
	}
}

func (in *jqInput) begin(name string, data []byte) {
	in.started = true
	in.curName = name
	if name == "-" {
		in.curName = "<stdin>"
	}
	in.data = data
	in.line = 0
	if in.opts.rawInput {
		in.lines = bytes.NewReader(data)
	} else {
		in.cur = &jsonParser{data: data, orders: in.orders}
	}
}

// countLines is the number of lines jq has read when it has parsed the text up
// to offset: it reads whole lines, so the line the value ends on counts when
// it ends with a newline.
func (in *jqInput) countLines(offset int) int {
	var end int
	if i := bytes.IndexByte(in.data[offset:], '\n'); i >= 0 {
		end = offset + i + 1
	} else {
		end = len(in.data)
	}
	return bytes.Count(in.data[:end], []byte{'\n'})
}

func (in *jqInput) slurp() (any, bool) {
	if in.opts.rawInput {
		var all []byte
		for _, name := range in.names {
			data, ok := in.readFile(name)
			if !ok {
				continue
			}
			in.begin(name, data)
			in.lines = nil
			all = append(all, data...)
		}
		in.line = bytes.Count(all, []byte{'\n'})
		return validUTF8(string(all)), true
	}
	out := []any{}
	for _, name := range in.names {
		data, ok := in.readFile(name)
		if !ok {
			continue
		}
		in.begin(name, data)
		for {
			v, ok, err := in.cur.next()
			if err != nil {
				in.line = in.countLines(in.cur.pos)
				return &jqParseError{err: err, pos: in.position()}, true
			}
			if !ok {
				break
			}
			out = append(out, v)
		}
		in.line = bytes.Count(data, []byte{'\n'})
		in.cur = nil
	}
	return out, true
}

// jqParseError is an input that is not JSON.
type jqParseError struct {
	err error
	pos string
}

func (e *jqParseError) Error() string { return "parse error: " + e.err.Error() }

// jqRefusedFuncs are the functions of jq that are refused, and why: they
// depend on the clock or on the machine, so the same input would not give the
// same output on every instance.
var jqRefusedFuncs = map[string]string{
	"now":           "it reads the clock",
	"localtime":     "it depends on the time zone of the machine; use gmtime",
	"strflocaltime": "it depends on the time zone of the machine; use strftime",
}

func isNameByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// jqRefusedCall finds a call of a refused function in the text of a filter. It
// reads the text as tokens: a name after "." or "$" is a field or a variable,
// a name before ":" is an object key, and text in a string is skipped except
// what a \( ) in it holds.
func jqRefusedCall(src string) (string, bool) {
	var scan func(i int, inInterpolation bool) (int, string)
	scan = func(i int, inInterpolation bool) (int, string) {
		depth := 0
		for i < len(src) {
			c := src[i]
			switch {
			case c == '#':
				for i < len(src) && src[i] != '\n' {
					i++
				}
			case c == '"':
				i++
				for i < len(src) && src[i] != '"' {
					if src[i] == '\\' && i+1 < len(src) {
						if src[i+1] == '(' {
							var found string
							i, found = scan(i+2, true)
							if found != "" {
								return i, found
							}
							continue
						}
						i++
					}
					i++
				}
				i++
			case c == '(':
				depth++
				i++
			case c == ')':
				if depth == 0 && inInterpolation {
					return i + 1, ""
				}
				depth--
				i++
			case isNameByte(c) && (c < '0' || c > '9'):
				start := i
				for i < len(src) && (isNameByte(src[i]) || src[i] == ':' && i+2 < len(src) && src[i+1] == ':' && isNameByte(src[i+2])) {
					if src[i] == ':' {
						i++
					}
					i++
				}
				name := src[start:i]
				if _, bad := jqRefusedFuncs[name]; !bad {
					continue
				}
				prev := byte(' ')
				if start > 0 {
					prev = src[start-1]
				}
				j := i
				for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n') {
					j++
				}
				if prev != '.' && prev != '$' && prev != '@' && (j >= len(src) || src[j] != ':') {
					return i, name
				}
			default:
				i++
			}
		}
		return i, ""
	}
	_, name := scan(0, false)
	return name, name != ""
}

func runJq(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(jqUsage)
	}
	orders := keyOrders{}
	o, status := parseJQArgs(c, orders)
	if status == -1 {
		return c.help(jqUsage)
	}
	if o == nil {
		return status
	}
	query, err := gojq.Parse(o.filter)
	if err != nil {
		return jqCompileError(c, err)
	}
	if len(query.Imports) > 0 {
		return jqCompileError(c, errors.New("modules (import, include) are not supported: there are no files to load them from"))
	}
	if name, bad := jqRefusedCall(o.filter); bad {
		return jqCompileError(c, fmt.Errorf("%s is not supported: %s, and the same input must give the same output on every instance", name, jqRefusedFuncs[name]))
	}
	in := &jqInput{c: c, opts: o, orders: orders}

	code, err := gojq.Compile(query,
		gojq.WithEnvironLoader(func() []string { return nil }),
		gojq.WithVariables(o.argNames),
		gojq.WithFunction("input_filename", 0, 0, func(any, []any) any { return in.filename() }),
		gojq.WithFunction("debug", 0, 0, func(v any, _ []any) any {
			enc := &jsonEncoder{w: c.Stderr, orders: orders}
			_, _ = io.WriteString(c.Stderr, `["DEBUG:",`)
			_ = enc.encode(v)
			_, _ = io.WriteString(c.Stderr, "]\n")
			return v
		}),
		gojq.WithFunction("stderr", 0, 0, func(v any, _ []any) any {
			enc := &jsonEncoder{w: c.Stderr, orders: orders}
			if s, ok := v.(string); ok {
				_, _ = io.WriteString(c.Stderr, s)
			} else {
				_ = enc.encode(v)
			}
			return v
		}),
		gojq.WithInputIter(in),
	)
	if err != nil {
		return jqCompileError(c, err)
	}

	runCtx, cancel := context.WithTimeout(ctx, jqRuntimeLimit)
	defer cancel()
	var heapStopped atomic.Bool
	stopWatch := watchHeap(jqHeapLimit, cancel, &heapStopped)
	defer stopWatch()

	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()
	enc := &jsonEncoder{w: out, orders: orders, indent: o.indent, sorted: o.sortKeys, ascii: o.ascii}

	const (
		okNoOutput = -4
		okNull     = -1
	)
	ret := okNoOutput
	lastResult := -1
	halted := false

	process := func(v any) int {
		result := okNoOutput
		iter := code.RunWithContext(runCtx, v, o.argValues...)
		for {
			got, ok := iter.Next()
			if !ok {
				return result
			}
			if err, isErr := got.(error); isErr {
				if halt, ok := errors.AsType[*gojq.HaltError](err); ok {
					halted = true
					if hv := halt.Value(); hv != nil {
						if s, isStr := hv.(string); isStr {
							_, _ = io.WriteString(c.Stderr, s)
						} else {
							_ = (&jsonEncoder{w: c.Stderr, orders: orders}).encode(hv)
							_, _ = io.WriteString(c.Stderr, "\n")
						}
					}
					return halt.ExitCode()
				}
				if runCtx.Err() != nil {
					return 5
				}
				jqRuntimeError(c, err, in.position(), orders)
				return 5
			}
			var werr error
			if s, isStr := got.(string); isStr && o.raw {
				_, werr = io.WriteString(out, s)
			} else {
				werr = enc.encode(got)
			}
			if werr == nil && !o.join {
				werr = out.WriteByte('\n')
			}
			if werr != nil {
				cancel()
				return 5
			}
			if got == nil || got == false {
				result = okNull
			} else {
				result = 0
			}
		}
	}

	if o.null {
		ret = process(nil)
		if ret <= 0 && ret != okNoOutput {
			lastResult = b2i(ret != okNull)
		}
	} else {
		for !halted && ctx.Err() == nil && runCtx.Err() == nil {
			v, ok := in.Next()
			if !ok {
				break
			}
			if pe, isPE := v.(*jqParseError); isPE {
				fmt.Fprintf(c.Stderr, "jq: parse error: %s\n", pe.err)
				ret = 5
				break
			}
			ret = process(v)
			if ret <= 0 && ret != okNoOutput {
				lastResult = b2i(ret != okNull)
			}
		}
	}
	if out.failed() || ctx.Err() != nil {
		return 1
	}
	if runCtx.Err() != nil {
		if heapStopped.Load() {
			fmt.Fprintf(c.Stderr, "jq: error: the program grew the heap by more than %d MiB and was stopped\n", jqHeapLimit>>20)
		} else {
			fmt.Fprintf(c.Stderr, "jq: error: the program ran for more than %s and was stopped\n", jqRuntimeLimit)
		}
		return 5
	}
	if in.failed && !halted {
		ret = 2
	}
	if o.exitStatus {
		if ret != okNoOutput {
			return abs(ret)
		}
		switch lastResult {
		case -1:
			return 4
		case 0:
			return 1
		}
		return 0
	}
	return max(ret, 0)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func jqCompileError(c *Call, err error) int {
	msg := err.Error()
	_, _ = fmt.Fprintf(c.Stderr, "jq: error: %s\njq: 1 compile error\n", msg)
	return 3
}

// jqRuntimeError prints an error of the filter as jq does: its message when
// the error value is a string, and the value as JSON otherwise.
func jqRuntimeError(c *Call, err error, pos string, orders keyOrders) {
	if ve, ok := errors.AsType[gojq.ValueError](err); ok {
		if s, ok := ve.Value().(string); ok {
			_, _ = fmt.Fprintf(c.Stderr, "jq: error (at %s): %s\n", pos, s)
			return
		}
		var sb strings.Builder
		_ = (&jsonEncoder{w: &sb, orders: orders}).encode(ve.Value())
		_, _ = fmt.Fprintf(c.Stderr, "jq: error (at %s) (not a string): %s\n", pos, sb.String())
		return
	}
	_, _ = fmt.Fprintf(c.Stderr, "jq: error (at %s): %s\n", pos, err.Error())
}

// watchHeap stops a program that grows the heap too far, by calling cancel. It
// samples the live heap of the process, which also counts what other work in
// the process holds, so it is a safety net and not an exact limit.
func watchHeap(limit uint64, cancel context.CancelFunc, stopped *atomic.Bool) (stop func()) {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(sample)
	base := sample[0].Value.Uint64()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				metrics.Read(sample)
				if sample[0].Value.Uint64() > base+limit {
					stopped.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}
