// Package events is the aictl side of the agent's server-sent events (agent.md section 3.10): a
// parser for the text/event-stream wire format and a small client that opens the stream with a
// type filter and a Last-Event-ID, and hands back parsed events. It knows nothing about transports:
// the caller supplies an OpenFunc (internal/cli binds it to the Client, so the unix socket, the TCP
// fallback and the startup retry are the ones every other command uses).
//
// The parser is tolerant on purpose (aictl.md section 3.3, "Progress"): it ignores unknown fields
// and unknown event types, accepts LF, CRLF and CR line ends, and keeps going after a frame whose
// data is not the JSON it expected.
package events

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// MaxFrameBytes bounds one line. The agent's frames are a few hundred bytes; a peer that sends a
// megabyte without a line end is broken and the stream is abandoned.
const MaxFrameBytes = 1 << 20

// ErrFrameTooLong is returned by Parser.Next when a line passes MaxFrameBytes.
var ErrFrameTooLong = errors.New("events: frame too long")

// Frame is one unit of the stream: either a comment line (the agent's ": ping" keepalive) or a
// dispatched event with its raw fields.
type Frame struct {
	// Comment is true for a ": text" line; Text holds the text after the colon, trimmed of one
	// leading space. No other field is set.
	Comment bool
	Text    string
	// ID is the value of the last "id:" line of the frame; HasID says whether the frame had one.
	// A frame without it (the synthetic resync, shutdown) does not move the client's Last-Event-ID.
	ID    string
	HasID bool
	// Event is the "event:" name ("" when the frame had none, which the standard calls "message").
	Event string
	// Data is the "data:" lines joined with "\n".
	Data string
}

// Parser reads frames from a text/event-stream body.
type Parser struct {
	br    *bufio.Reader
	first bool

	data    []string
	hasData bool
	event   string
	id      string
	hasID   bool
}

// NewParser returns a Parser over r.
func NewParser(r io.Reader) *Parser {
	return &Parser{br: bufio.NewReaderSize(r, 4096), first: true}
}

// Next returns the next comment or event frame. It returns io.EOF at the end of the stream; a frame
// still open at the end (no blank line after it) is discarded, as the standard says. Any other
// error is the reader's own (a dropped connection) or ErrFrameTooLong.
func (p *Parser) Next() (Frame, error) {
	for {
		line, err := p.readLine()
		if err != nil {
			return Frame{}, err
		}
		if len(line) == 0 { // a blank line dispatches the frame
			if f, ok := p.dispatch(); ok {
				return f, nil
			}
			continue
		}
		if line[0] == ':' {
			return Frame{Comment: true, Text: strings.TrimPrefix(line[1:], " ")}, nil
		}
		p.field(line)
	}
}

func (p *Parser) dispatch() (Frame, bool) {
	defer func() {
		p.data, p.hasData, p.event, p.id, p.hasID = nil, false, "", "", false
	}()
	if !p.hasData {
		return Frame{}, false // an "id:" or "event:" alone is not an event
	}
	return Frame{ID: p.id, HasID: p.hasID, Event: p.event, Data: strings.Join(p.data, "\n")}, true
}

func (p *Parser) field(line string) {
	name, value, found := strings.Cut(line, ":")
	if found {
		value = strings.TrimPrefix(value, " ")
	}
	switch name {
	case "data":
		p.data = append(p.data, value)
		p.hasData = true
	case "event":
		p.event = value
	case "id":
		if !strings.ContainsRune(value, 0) { // the standard ignores an id with a NUL
			p.id, p.hasID = value, true
		}
	}
	// "retry" and unknown fields are ignored.
}

// readLine returns one line without its terminator (LF, CRLF or a lone CR). EOF in the middle of a
// line drops the partial line.
func (p *Parser) readLine() (string, error) {
	var buf bytes.Buffer
	for {
		b, err := p.br.ReadByte()
		if err != nil {
			return "", err
		}
		switch b {
		case '\n':
			return p.trimBOM(buf.String()), nil
		case '\r':
			if next, perr := p.br.Peek(1); perr == nil && next[0] == '\n' {
				_, _ = p.br.ReadByte()
			}
			return p.trimBOM(buf.String()), nil
		}
		if buf.Len() >= MaxFrameBytes {
			return "", ErrFrameTooLong
		}
		buf.WriteByte(b)
	}
}

// trimBOM drops a UTF-8 byte order mark at the very start of the stream.
func (p *Parser) trimBOM(s string) string {
	if p.first {
		p.first = false
		return strings.TrimPrefix(s, "\xef\xbb\xbf")
	}
	return s
}
