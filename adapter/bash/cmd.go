package bash

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"syscall"
)

// Call is one run of a command: its arguments (without the name), the streams
// it reads and writes, and the file system it sees.
type Call struct {
	Args   []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	FS     fs.FS
	// Dir is the working directory, which relative paths are joined to.
	Dir string
}

type command struct {
	run func(ctx context.Context, c *Call) int
}

// commands is the set of commands Run supports.
var commands map[string]command

func init() {
	commands = map[string]command{
		"cat":    {run: runCat},
		"head":   {run: runHead},
		"grep":   {run: runGrep},
		"tail":   {run: runTail},
		"sed":    {run: runSed},
		"wc":     {run: runWc},
		"sort":   {run: runSort},
		"uniq":   {run: runUniq},
		"cut":    {run: runCut},
		"tr":     {run: runTr},
		"base64": {run: runBase64},
		"echo":   {run: runEcho},
		"ls":     {run: runLs},
	}
}

// errf writes a message of command name to stderr.
func (c *Call) errf(name, format string, args ...any) {
	_, _ = fmt.Fprintf(c.Stderr, "%s: %s\n", name, fmt.Sprintf(format, args...))
}

// open opens a named input for reading: "-" is standard input. A directory is
// an error, as it is for the GNU tools when they read it.
func (c *Call) open(name string) (io.ReadCloser, error) {
	if name == "-" {
		return io.NopCloser(c.Stdin), nil
	}
	file, err := c.FS.Open(fsName(c.Dir, name))
	if err != nil {
		return nil, errnoOf(err)
	}
	if info, err := file.Stat(); err == nil && info.IsDir() {
		_ = file.Close()
		return nil, syscall.EISDIR
	}
	return file, nil
}

// errText is the text GNU tools print for an error.
func errText(err error) string {
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		switch errno {
		case syscall.ENOENT:
			return "No such file or directory"
		case syscall.EISDIR:
			return "Is a directory"
		case syscall.EACCES:
			return "Permission denied"
		case syscall.EINVAL:
			return "Invalid argument"
		}
	}
	if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
		return errText(pathErr.Err)
	}
	return err.Error()
}

// output is a buffered stdout. A failed write is kept: the command stops at
// the next check, as one does when the reader of its pipe has closed.
type output struct {
	*bufio.Writer
}

func newOutput(w io.Writer) *output { return &output{bufio.NewWriterSize(w, 32<<10)} }

// failed reports whether a write or a flush has failed.
func (o *output) failed() bool {
	_, err := o.Write(nil)
	return err != nil
}

// optSpec is one option of a command: -short, --long, and whether it takes a
// value.
type optSpec struct {
	short byte
	long  string
	arg   bool
}

func (s optSpec) id() string {
	if s.long != "" {
		return s.long
	}
	return string(s.short)
}

func (s optSpec) String() string {
	var parts []string
	if s.short != 0 {
		parts = append(parts, "-"+string(s.short))
	}
	if s.long != "" {
		parts = append(parts, "--"+s.long)
	}
	return strings.Join(parts, "/")
}

// parsedOpt is an option found in the arguments: id is the long name, or the
// short letter when there is none.
type parsedOpt struct {
	id    string
	value string
}

// optError is why the arguments do not parse; it reads as a message of the
// command.
type optError struct {
	msg string
}

func (e *optError) Error() string { return e.msg }

// getopt parses args as GNU getopt_long does: short options may be grouped
// (-in), a value may follow the letter or be the next argument, a long option
// may be shortened to a unique prefix, "--" ends the options, and options and
// operands may be mixed. It returns the options in order and the operands. An
// option that is not in specs is an error that lists the ones that are.
func getopt(name string, specs []optSpec, args []string) ([]parsedOpt, []string, error) {
	supported := func() string {
		list := make([]string, 0, len(specs)+1)
		for _, s := range specs {
			list = append(list, s.String())
		}
		return strings.Join(append(list, "--help"), ", ")
	}
	unsupported := func(what string) error {
		return &optError{fmt.Sprintf("%s: option %s is not supported. Supported options: %s. Try '%s --help'", name, what, supported(), name)}
	}
	var opts []parsedOpt
	var operands []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			operands = append(operands, args[i+1:]...)
			return opts, operands, nil
		case strings.HasPrefix(arg, "--"):
			key, value, hasValue := strings.Cut(arg[2:], "=")
			var match []optSpec
			for _, s := range specs {
				if s.long == key {
					match = []optSpec{s}
					break
				}
				if s.long != "" && strings.HasPrefix(s.long, key) {
					match = append(match, s)
				}
			}
			if len(match) > 1 {
				return nil, nil, &optError{fmt.Sprintf("%s: option '--%s' is ambiguous. Try '%s --help'", name, key, name)}
			}
			if len(match) == 0 {
				return nil, nil, unsupported("'--" + key + "'")
			}
			spec := match[0]
			switch {
			case spec.arg && !hasValue:
				if i+1 >= len(args) {
					return nil, nil, &optError{fmt.Sprintf("%s: option '--%s' requires an argument. Try '%s --help'", name, spec.long, name)}
				}
				i++
				value = args[i]
			case !spec.arg && hasValue:
				return nil, nil, &optError{fmt.Sprintf("%s: option '--%s' doesn't allow an argument. Try '%s --help'", name, spec.long, name)}
			}
			opts = append(opts, parsedOpt{id: spec.id(), value: value})
		case len(arg) > 1 && arg[0] == '-':
			for j := 1; j < len(arg); j++ {
				var spec *optSpec
				for k := range specs {
					if specs[k].short == arg[j] {
						spec = &specs[k]
						break
					}
				}
				if spec == nil {
					return nil, nil, unsupported("'-" + string(arg[j]) + "'")
				}
				if !spec.arg {
					opts = append(opts, parsedOpt{id: spec.id()})
					continue
				}
				value := arg[j+1:]
				if value == "" {
					if i+1 >= len(args) {
						return nil, nil, &optError{fmt.Sprintf("%s: option requires an argument -- '%c'. Try '%s --help'", name, arg[j], name)}
					}
					i++
					value = args[i]
				}
				opts = append(opts, parsedOpt{id: spec.id(), value: value})
				break
			}
		default:
			operands = append(operands, arg)
		}
	}
	return opts, operands, nil
}

// help prints the usage of a command and returns the exit status 0. The usage
// lists exactly the options the command supports.
func (c *Call) help(usage string) int {
	_, _ = io.WriteString(c.Stdout, usage)
	return 0
}

// hasHelp reports whether the arguments ask for --help before a "--".
func hasHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--help" {
			return true
		}
	}
	return false
}
