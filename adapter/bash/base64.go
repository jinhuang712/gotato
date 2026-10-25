package bash

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
)

const base64Usage = `Usage: base64 [OPTION]... [FILE]
Base64 encode or decode FILE, or standard input, to standard output.
With no FILE, or when FILE is -, read standard input.

Supported options:
  -d, --decode          decode data
  -i, --ignore-garbage  when decoding, ignore non-alphabet characters
  -w, --wrap=COLS       wrap encoded lines after COLS characters (default 76);
                        use 0 to disable line wrapping
      --help            display this help and exit
`

var base64Options = []optSpec{
	{short: 'd', long: "decode"},
	{short: 'i', long: "ignore-garbage"},
	{short: 'w', long: "wrap", arg: true},
}

func runBase64(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(base64Usage)
	}
	opts, operands, err := getopt("base64", base64Options, c.Args)
	if err != nil {
		return c.fail(err, 1)
	}
	var decode, ignoreGarbage bool
	wrap := uint64(76)
	for _, opt := range opts {
		switch opt.id {
		case "decode":
			decode = true
		case "ignore-garbage":
			ignoreGarbage = true
		case "wrap":
			if !isDigits(opt.value) {
				c.errf("base64", "invalid wrap size: '%s'", opt.value)
				return 1
			}
			wrap = parseUint(opt.value)
		}
	}
	if len(operands) > 1 {
		c.errf("base64", "extra operand '%s'", operands[1])
		c.tryHelp("base64")
		return 1
	}
	name := "-"
	if len(operands) == 1 {
		name = operands[0]
	}
	in, err := c.open(name)
	if err != nil {
		c.errf("base64", "%s: %s", quoteName(name), errText(err))
		return 1
	}
	defer func() { _ = in.Close() }()
	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()

	var rerr error
	valid := true
	if decode {
		valid, rerr = base64Decode(ctx, in, out, ignoreGarbage)
	} else {
		rerr = base64Encode(ctx, in, out, wrap)
	}
	if rerr != nil {
		if out.failed() || ctx.Err() != nil {
			return 1
		}
		c.errf("base64", "read error: %s", errText(rerr))
		return 1
	}
	if err := out.Flush(); err != nil {
		return 1
	}
	if !valid {
		c.errf("base64", "invalid input")
		return 1
	}
	return 0
}

// base64Encode writes the encoding of in, in lines of wrap characters (no
// line breaks at all when wrap is 0), ending the last line with a newline.
func base64Encode(ctx context.Context, in io.Reader, out *output, wrap uint64) error {
	const chunk = 3 * 4096
	buf := make([]byte, chunk)
	enc := make([]byte, base64.StdEncoding.EncodedLen(chunk))
	col := uint64(0)
	emit := func(text []byte) error {
		for len(text) > 0 {
			if wrap == 0 {
				_, err := out.Write(text)
				return err
			}
			n := min(uint64(len(text)), wrap-col)
			if _, err := out.Write(text[:n]); err != nil {
				return err
			}
			text, col = text[n:], col+n
			if col == wrap {
				if err := out.WriteByte('\n'); err != nil {
					return err
				}
				col = 0
			}
		}
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(in, buf)
		if n > 0 {
			base64.StdEncoding.Encode(enc, buf[:n])
			if werr := emit(enc[:base64.StdEncoding.EncodedLen(n)]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return err
		}
	}
	if wrap > 0 && col > 0 {
		return out.WriteByte('\n')
	}
	return nil
}

func base64Value(b byte) int {
	switch {
	case b >= 'A' && b <= 'Z':
		return int(b - 'A')
	case b >= 'a' && b <= 'z':
		return int(b-'a') + 26
	case b >= '0' && b <= '9':
		return int(b-'0') + 52
	case b == '+':
		return 62
	case b == '/':
		return 63
	}
	return -1
}

// base64Decode decodes in quartet by quartet, as GNU base64 does: a newline
// is skipped (and, with ignoreGarbage, any other byte that is not of the
// alphabet), a quartet may end in = or ==, and the next quartet starts fresh.
// It reports false for input that is not valid, after writing what decoded.
func base64Decode(ctx context.Context, in io.Reader, out *output, ignoreGarbage bool) (bool, error) {
	var quad [4]byte
	n := 0
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		got, rerr := in.Read(buf)
		for _, b := range buf[:got] {
			if b == '\n' {
				continue
			}
			if base64Value(b) < 0 && b != '=' {
				if ignoreGarbage {
					continue
				}
				return false, nil
			}
			quad[n] = b
			n++
			if n < 4 {
				continue
			}
			n = 0
			v0, v1 := base64Value(quad[0]), base64Value(quad[1])
			if v0 < 0 || v1 < 0 {
				return false, nil
			}
			switch {
			case quad[2] == '=':
				if quad[3] != '=' {
					return false, nil
				}
				_ = out.WriteByte(byte(v0<<2 | v1>>4))
			case quad[3] == '=':
				v2 := base64Value(quad[2])
				_, _ = out.Write([]byte{byte(v0<<2 | v1>>4), byte(v1<<4 | v2>>2)})
			default:
				v2, v3 := base64Value(quad[2]), base64Value(quad[3])
				_, _ = out.Write([]byte{byte(v0<<2 | v1>>4), byte(v1<<4 | v2>>2), byte(v2<<6 | v3)})
			}
			if out.failed() {
				return true, io.ErrClosedPipe
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				// GNU writes what a last group cut short still holds, then
				// reports the input invalid.
				if n >= 2 && quad[0] != '=' && quad[1] != '=' && (n == 2 || quad[2] != '=') {
					v0, v1 := base64Value(quad[0]), base64Value(quad[1])
					_ = out.WriteByte(byte(v0<<2 | v1>>4))
					if n == 3 {
						_ = out.WriteByte(byte(v1<<4 | base64Value(quad[2])>>2))
					}
				}
				return n == 0, nil
			}
			return true, rerr
		}
	}
}
