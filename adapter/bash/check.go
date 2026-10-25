package bash

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Refusal is why a script was not run: it does not parse, or it uses syntax or
// a command that the subset does not have. Its message names the place in the
// script and suggests what to write instead.
type Refusal struct {
	msg  string
	code int
}

func (r *Refusal) Error() string { return r.msg }

// ExitCode is the exit status Run reports for the refusal: 127 for a command
// that is not supported, as bash does, and 2 for anything else.
func (r *Refusal) ExitCode() int { return r.code }

const hintPipeline = "this is a read-only bash subset: use a pipeline of the commands (%s), joined with | && || ;"

func refuse(pos syntax.Pos, format string, args ...any) *Refusal {
	where := ""
	if pos.IsValid() {
		where = fmt.Sprintf("line %d, column %d: ", pos.Line(), pos.Col())
	}
	return &Refusal{msg: "bash: " + where + fmt.Sprintf(format, args...), code: 2}
}

func pipelineHint() string { return fmt.Sprintf(hintPipeline, strings.Join(Commands(), ", ")) }

// maxBraceWords is the most words one brace expansion, such as {1..9}, may
// make: it bounds the memory an expansion takes.
const maxBraceWords = 10000

// Check parses command as bash and refuses what the subset does not have,
// before anything runs. It returns the syntax tree to run, or a *Refusal.
//
// Allowed: simple commands, pipes (| and |&), &&, ||, ;, newlines, ! , { },
// ( ), if and case, quotes and escapes, globs and brace expansion, parameter
// expansion of the variables the script sets, assignments with a literal
// value, a here-document or here-string on the input, an input redirect from
// the file system, a duplicated descriptor (2>&1), and output sent to
// /dev/null.
//
// Refused: for, while, until and select loops; functions; command
// substitution ($( ) and backticks); process substitution; arithmetic, in a
// command, an expansion or an index; [[ ]], let, time, coproc; declare,
// export, local and readonly; a command in the background (&); a redirect that
// writes anywhere but /dev/null; a parameter that differs from run to run
// ($$, PPID, RANDOM, SRANDOM) or that reads another (${!x}, ${x[i]}); an
// assignment whose value expands a parameter (it could double a value without
// end); and a command that is not one of Commands.
func Check(command string) (*syntax.File, *Refusal) {
	if len(command) > MaxCommandBytes {
		return nil, &Refusal{msg: fmt.Sprintf("bash: the command is %d bytes; the most is %d", len(command), MaxCommandBytes), code: 2}
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, &Refusal{msg: "bash: syntax error: " + err.Error(), code: 2}
	}
	var refusal *Refusal
	syntax.Walk(file, func(node syntax.Node) bool {
		if refusal != nil {
			return false
		}
		refusal = checkNode(node)
		return refusal == nil
	})
	if refusal != nil {
		return nil, refusal
	}
	return file, nil
}

func checkNode(node syntax.Node) *Refusal {
	switch n := node.(type) {
	case *syntax.ForClause:
		if n.Select {
			return refuse(n.Pos(), "a select loop is not supported; %s", pipelineHint())
		}
		return refuse(n.Pos(), "a for loop is not supported; %s. To act on several files, name them or use a glob: grep x /results/*", pipelineHint())
	case *syntax.WhileClause:
		kind := "while"
		if n.Until {
			kind = "until"
		}
		return refuse(n.Pos(), "a %s loop is not supported; %s", kind, pipelineHint())
	case *syntax.FuncDecl:
		return refuse(n.Pos(), "a function is not supported; %s", pipelineHint())
	case *syntax.CmdSubst:
		return refuse(n.Pos(), "command substitution ($( ) or backticks) is not supported; %s. Read the file in the command that needs it, or pipe into it", pipelineHint())
	case *syntax.ProcSubst:
		return refuse(n.Pos(), "process substitution is not supported; %s", pipelineHint())
	case *syntax.ArithmCmd:
		return refuse(n.Pos(), "an arithmetic command (( )) is not supported; %s", pipelineHint())
	case *syntax.ArithmExp:
		return refuse(n.Pos(), "arithmetic expansion is not supported; %s", pipelineHint())
	case *syntax.LetClause:
		return refuse(n.Pos(), "let is not supported; %s", pipelineHint())
	case *syntax.TestClause:
		return refuse(n.Pos(), "[[ ]] is not supported; %s", pipelineHint())
	case *syntax.TimeClause:
		return refuse(n.Pos(), "time is not supported (the clock is not available); %s", pipelineHint())
	case *syntax.CoprocClause:
		return refuse(n.Pos(), "coproc is not supported; %s", pipelineHint())
	case *syntax.TestDecl:
		return refuse(n.Pos(), "a test declaration is not supported; %s", pipelineHint())
	case *syntax.DeclClause:
		return refuse(n.Pos(), "%s is not supported: a variable is set with name=value and used as $name; %s", n.Variant.Value, pipelineHint())
	case *syntax.Stmt:
		if n.Background || n.Coprocess || n.Disown {
			return refuse(n.Pos(), "a command in the background (&) is not supported; %s", pipelineHint())
		}
		for _, rd := range n.Redirs {
			if r := checkRedirect(rd); r != nil {
				return r
			}
		}
	case *syntax.CallExpr:
		return checkCall(n)
	case *syntax.ParamExp:
		return checkParam(n)
	case *syntax.Word:
		return checkBraces(n)
	}
	return nil
}

// checkCall refuses a command that is not supported, by its name when the name
// is a literal, and an assignment whose value expands a parameter.
func checkCall(call *syntax.CallExpr) *Refusal {
	for _, assign := range call.Assigns {
		if assign.Value == nil && assign.Array == nil && assign.Index == nil {
			continue
		}
		if assign.Index != nil || assign.Array != nil || assign.Append {
			return refuse(assign.Pos(), "only name=value assignments are supported")
		}
		if hasParam(assign.Value) {
			return refuse(assign.Pos(), "an assignment cannot use a variable in its value (name=$other); write the value out, or use the variable where it is needed")
		}
	}
	if len(call.Args) == 0 {
		return nil
	}
	name := call.Args[0].Lit()
	if name == "" {
		return nil // not a literal: callHandler and the exec handler decide when it runs
	}
	if _, ok := commands[name]; ok {
		return nil
	}
	return &Refusal{msg: unknownCommand(name), code: 127}
}

// unknownCommand is the message for a command that is not supported, with the
// reason for the ones a model reaches for to escape the subset, and the list
// of the commands that are.
func unknownCommand(name string) string {
	return "bash: " + notSupportedWhy(name) + " The commands are: " + strings.Join(Commands(), ", ") + "."
}

func notSupportedWhy(name string) string {
	switch name {
	case "eval", "source", ".", "exec", "trap", "command", "builtin", "enable":
		return name + ": not supported, since it would run something other than the commands below."
	case "cd", "pushd", "popd":
		return name + ": not supported; the working directory is / and paths are written in full."
	case "export", "unset", "set", "shopt", "alias", "unalias", "read", "wait", "kill", "umask", "ulimit", "hash", "type", "test", "[":
		return name + ": not supported; the environment of the script is fixed."
	}
	return name + ": command not found."
}

func hasParam(word *syntax.Word) bool {
	found := false
	syntax.Walk(word, func(node syntax.Node) bool {
		if _, ok := node.(*syntax.ParamExp); ok {
			found = true
		}
		return !found
	})
	return found
}

func checkRedirect(rd *syntax.Redirect) *Refusal {
	switch rd.Op {
	case syntax.RdrIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		return nil
	case syntax.DplIn, syntax.DplOut:
		if arg := rd.Word.Lit(); arg == "-" || arg == "1" || arg == "2" {
			return nil
		}
		return refuse(rd.Pos(), "this redirect is not supported; 2>&1 is")
	case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll:
		if rd.Word.Lit() == "/dev/null" {
			return nil
		}
		return refuse(rd.Pos(), "a redirect that writes a file is not supported: nothing can be written, and the output is returned to you. Only > /dev/null is allowed")
	}
	return refuse(rd.Pos(), "this redirect is not supported; a redirect may read (< file), duplicate (2>&1) or discard (> /dev/null)")
}

// varying are the parameters whose value is not the same on every run.
var varying = map[string]bool{"$": true, "PPID": true, "RANDOM": true, "SRANDOM": true, "BASHPID": true}

func checkParam(p *syntax.ParamExp) *Refusal {
	if p.Param != nil && varying[p.Param.Value] {
		return refuse(p.Pos(), "$%s is not supported: it differs from run to run, and the same data must give the same output", p.Param.Value)
	}
	switch {
	case p.Excl:
		return refuse(p.Pos(), "${!name} is not supported")
	case p.Names != 0:
		return refuse(p.Pos(), "${!prefix*} is not supported")
	case p.Index != nil:
		return refuse(p.Pos(), "array indexes are not supported")
	case p.Exp != nil && p.Exp.Op == syntax.OtherParamOps:
		return refuse(p.Pos(), "${name@op} is not supported")
	case p.Slice != nil:
		for _, expr := range []syntax.ArithmExpr{p.Slice.Offset, p.Slice.Length} {
			if expr == nil {
				continue
			}
			word, ok := expr.(*syntax.Word)
			if !ok {
				return refuse(p.Pos(), "${name:offset:length} takes plain numbers")
			}
			if _, err := strconv.Atoi(word.Lit()); err != nil {
				return refuse(p.Pos(), "${name:offset:length} takes plain numbers")
			}
		}
	}
	return nil
}

// checkBraces splits the brace expansions of a copy of a word, as the
// interpreter will, and refuses one that would make more than maxBraceWords words.
func checkBraces(word *syntax.Word) *Refusal {
	split := &syntax.Word{Parts: slices.Clone(word.Parts)}
	if !syntax.SplitBraces(split) {
		return nil
	}
	if n := wordCount(split); n > maxBraceWords {
		return refuse(word.Pos(), "this brace expansion makes %d words; the most is %d", n, maxBraceWords)
	}
	return nil
}

// wordCount is how many words a word expands to by braces alone.
func wordCount(word *syntax.Word) int {
	total := 1
	for _, part := range word.Parts {
		brace, ok := part.(*syntax.BraceExp)
		if !ok {
			continue
		}
		n := 0
		if brace.Sequence {
			n = sequenceLength(brace)
		} else {
			for _, elem := range brace.Elems {
				n += wordCount(elem)
			}
		}
		total *= max(n, 1)
		if total > 1<<30 {
			return total
		}
	}
	return total
}

// sequenceLength is how many words {from..to..step} makes; 0 when it does not
// parse, since it is then left as it was written.
func sequenceLength(brace *syntax.BraceExp) int {
	if len(brace.Elems) < 2 {
		return 0
	}
	from, to, step := brace.Elems[0].Lit(), brace.Elems[1].Lit(), 1
	if len(brace.Elems) > 2 {
		if v, err := strconv.Atoi(brace.Elems[2].Lit()); err == nil && v != 0 {
			step = max(v, -v)
		}
	}
	a, errA := strconv.Atoi(from)
	b, errB := strconv.Atoi(to)
	if errA != nil || errB != nil {
		if len(from) == 1 && len(to) == 1 { // {a..z}
			return max(int(from[0]), int(to[0])) - min(int(from[0]), int(to[0])) + 1
		}
		return 0
	}
	return (max(a, b)-min(a, b))/step + 1
}
