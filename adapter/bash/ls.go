package bash

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

const lsUsage = `Usage: ls [OPTION]... [FILE]...
List information about the FILEs (the root directory by default). Entries are
sorted by name (by code point), and written one per line, as GNU ls does when
its output is not a terminal.

Supported options:
  -1                   list one file per line (always the case here)
  -a, --all            do not ignore entries starting with .
  -A, --almost-all     do not list implied . and ..
  -d, --directory      list directories themselves, not their contents
  -h, --human-readable with -l, print sizes like 1K 234M 2G
  -l                   use a long listing format
  -r, --reverse        reverse order while sorting
  -S                   sort by file size, largest first
      --help           display this help and exit

In the long format every file has the mode, the owner and the modification time
the file system gives it (a result is read-only, root:root, and has the Unix
epoch as its time), the time is always written with its year, and the total is
counted in 4 KiB blocks.
`

var lsOptions = []optSpec{
	{short: '1'},
	{short: 'a', long: "all"},
	{short: 'A', long: "almost-all"},
	{short: 'd', long: "directory"},
	{short: 'h', long: "human-readable"},
	{short: 'l'},
	{short: 'r', long: "reverse"},
	{short: 'S'},
}

type lsEntry struct {
	name string
	info fs.FileInfo
}

type lsFlags struct {
	long, human, reverse, bySize bool
}

func runLs(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(lsUsage)
	}
	opts, operands, err := getopt("ls", lsOptions, c.Args)
	if err != nil {
		return c.fail(err, 2)
	}
	var f lsFlags
	var all, almost, dirsAsFiles bool
	for _, opt := range opts {
		switch opt.id {
		case "all":
			all = true
		case "almost-all":
			almost = true
		case "directory":
			dirsAsFiles = true
		case "human-readable":
			f.human = true
		case "l":
			f.long = true
		case "reverse":
			f.reverse = true
		case "S":
			f.bySize = true
		}
	}
	if len(operands) == 0 {
		operands = []string{"."}
	}
	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()
	status := 0

	var files, dirs []lsEntry
	for _, name := range operands {
		info, err := fs.Stat(c.FS, fsName(c.Dir, name))
		if err != nil {
			c.errf("ls", "cannot access %s: %s", quoteAlways(name), errText(errnoOf(err)))
			status = 2
			continue
		}
		if info.IsDir() && !dirsAsFiles {
			dirs = append(dirs, lsEntry{name, info})
		} else {
			files = append(files, lsEntry{name, info})
		}
	}
	lsSort(files, f)
	lsSort(dirs, f)
	wrote := false
	if len(files) > 0 {
		lsPrint(out, files, f, false)
		wrote = true
	}
	for _, d := range dirs {
		if err := ctx.Err(); err != nil {
			return 2
		}
		if wrote {
			_ = out.WriteByte('\n')
		}
		if len(operands) > 1 {
			_, _ = out.WriteString(d.name + ":\n")
		}
		wrote = true
		entries, err := fs.ReadDir(c.FS, fsName(c.Dir, d.name))
		if err != nil {
			c.errf("ls", "cannot open directory %s: %s", quoteAlways(d.name), errText(errnoOf(err)))
			status = 2
			continue
		}
		var list []lsEntry
		if all {
			list = append(list, lsEntry{".", d.info}, lsEntry{"..", d.info})
		}
		for _, e := range entries {
			if !all && !almost && strings.HasPrefix(e.Name(), ".") {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				c.errf("ls", "cannot access %s: %s", quoteAlways(strings.TrimSuffix(d.name, "/")+"/"+e.Name()), errText(errnoOf(ierr)))
				status = 2
				continue
			}
			list = append(list, lsEntry{e.Name(), info})
		}
		lsSort(list, f)
		lsPrint(out, list, f, true)
		if out.failed() {
			return 2
		}
	}
	if err := out.Flush(); err != nil {
		return 2
	}
	return status
}

// lsSort orders entries by name in code point order, or by size (largest
// first) and then by name, and reverses the order for -r.
func lsSort(list []lsEntry, f lsFlags) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		less := a.name < b.name
		if f.bySize && a.info.Size() != b.info.Size() {
			less = a.info.Size() > b.info.Size()
		}
		if f.reverse {
			if f.bySize && a.info.Size() != b.info.Size() {
				return a.info.Size() < b.info.Size()
			}
			return a.name > b.name
		}
		return less
	})
}

func lsMode(m fs.FileMode) string {
	kind := byte('-')
	switch {
	case m.IsDir():
		kind = 'd'
	case m&fs.ModeSymlink != 0:
		kind = 'l'
	}
	perm := []byte("---------")
	for i := range 9 {
		if m.Perm()&(1<<(8-i)) != 0 {
			perm[i] = "rwxrwxrwx"[i]
		}
	}
	return string(kind) + string(perm)
}

func lsPrint(out *output, list []lsEntry, f lsFlags, withTotal bool) {
	if !f.long {
		for _, e := range list {
			_, _ = out.WriteString(e.name)
			_ = out.WriteByte('\n')
		}
		return
	}
	sizes := make([]string, len(list))
	links := make([]string, len(list))
	sizeW, linkW := 0, 0
	var blocks uint64
	for i, e := range list {
		size := uint64(max(e.info.Size(), 0))
		if f.human {
			sizes[i] = humanSize(size)
		} else {
			sizes[i] = strconv.FormatUint(size, 10)
		}
		n := 1
		if e.info.IsDir() {
			n = 2
		}
		links[i] = strconv.Itoa(n)
		sizeW, linkW = max(sizeW, len(sizes[i])), max(linkW, len(links[i]))
		blocks += (size + 4095) / 4096 * 4
	}
	if withTotal {
		total := strconv.FormatUint(blocks, 10)
		if f.human {
			total = humanSize(blocks * 1024)
		}
		_, _ = out.WriteString("total " + total + "\n")
	}
	for i, e := range list {
		_, _ = fmt.Fprintf(out, "%s %*s root root %*s %s %s\n", lsMode(e.info.Mode()), linkW, links[i], sizeW, sizes[i],
			e.info.ModTime().UTC().Format("Jan _2  2006"), e.name)
	}
}

// humanSize writes a size as ls -h does: in powers of 1024, rounded up, with
// one decimal below 10.
func humanSize(n uint64) string {
	if n < 1024 {
		return strconv.FormatUint(n, 10)
	}
	const units = "KMGTPE"
	div := uint64(1024)
	for exp := range len(units) {
		tenths := (n*10 + div - 1) / div
		if tenths < 100 {
			return fmt.Sprintf("%d.%d%c", tenths/10, tenths%10, units[exp])
		}
		whole := (n + div - 1) / div
		if whole < 1024 || exp == len(units)-1 {
			return fmt.Sprintf("%d%c", whole, units[exp])
		}
		div *= 1024
	}
	return strconv.FormatUint(n, 10)
}
