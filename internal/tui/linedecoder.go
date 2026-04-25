package tui

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"
)

// lineDecoder reads newline-delimited JSON values from a shared reader.
// A single decoder instance is kept per connection so that bufio.Scanner
// internal buffering is preserved across Decode calls.
type lineDecoder struct {
	scanner *bufio.Scanner
}

// Global cache keyed by the reader so tea.Cmds can re-create a decoder that
// continues from where the previous read left off. bufio.Scanner buffers
// bytes internally, so a fresh scanner on each read would drop data that
// came in together with the previous line.
var (
	decMu sync.Mutex
	decs  = map[io.Reader]*lineDecoder{}
)

func newLineDecoder(r io.Reader) *lineDecoder {
	decMu.Lock()
	defer decMu.Unlock()
	if d, ok := decs[r]; ok {
		return d
	}
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	d := &lineDecoder{scanner: s}
	decs[r] = d
	return d
}

func (d *lineDecoder) Decode(v any) error {
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return err
		}
		return io.EOF
	}
	return json.Unmarshal(d.scanner.Bytes(), v)
}
