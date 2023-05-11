// Package sse implements a user agent for the Server-Sent Events Protocol https://www.w3.org/TR/2015/REC-eventsource-20150203/
//
// The major parts of the protocol that aren't implemented are reestablishing the connection and some of the error events outlined in https://www.w3.org/TR/2015/REC-eventsource-20150203/#processing-model.
package sse

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/pkg/errors"
	"golang.org/x/text/encoding"
	"golang.org/x/text/transform"
)

var utf8BOM = []byte{0xfe, 0xff}
var eventType = []byte("event")
var dataType = []byte("data")
var idType = []byte("id")
var retryType = []byte("retry")

// Event represents a Server-Sent Event
type Event struct {
	Type string
	Data string
}

// Stream reads and parses events from a resource
type Stream struct {
	resource   string
	events     chan Event
	httpClient *http.Client

	reconnectionTime int
	data             *bytes.Buffer
	eventType        *bytes.Buffer
	lastEventID      *bytes.Buffer
}

// New constructs a Stream for a resource
//
// Errors generated from creating the initial connection are returned.
// Events are read from the channel returned by Stream.Events
func New(resource string) (Stream, error) {
	s := Stream{
		resource:    resource,
		events:      make(chan Event),
		httpClient:  http.DefaultClient,
		data:        new(bytes.Buffer),
		eventType:   new(bytes.Buffer),
		lastEventID: new(bytes.Buffer),
	}

	r, err := s.connect()
	if err != nil {
		return s, err
	}

	go s.parse(r)

	return s, nil
}

// Events returns a channel to read the event stream
func (s Stream) Events() <-chan Event {
	return s.events
}

func (s Stream) connect() (io.ReadCloser, error) {
	req, err := http.NewRequest(http.MethodGet, s.resource, nil)
	if err != nil {
		return nil, errors.Wrap(err, "creating http request")
	}

	req.Header.Add("Content-Type", "text/event-stream")
	req.Header.Add("Cache-Control", "no-cache")
	if s.lastEventID.Len() != 0 {
		req.Header.Add("Last-Event-ID", s.lastEventID.String())
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "http error")
	}

	// TODO: other status codes
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("unexpected status code %v", resp.StatusCode)
	}

	return resp.Body, nil
}

func splitLines(data []byte, atEOF bool) (int, []byte, error) {
	// Lines must be separated by either a U+000D CARRIAGE RETURN U+000A LINE FEED (CRLF) character pair, a single U+000A LINE FEED (LF) character, or a single U+000D CARRIAGE RETURN (CR) character.
	for i, b := range data {
		switch b {
		case '\r':
			if i+1 >= len(data) {
				return 0, nil, nil
			}
			if data[i+1] == '\n' {
				return i + 2, data[:i+1], nil
			}
			return i + 1, data[:i], nil
		case '\n':
			return i + 1, data[:i], nil

		}
	}
	return 0, nil, nil
}

func (s *Stream) parse(reader io.ReadCloser) error {
	// TODO: reconnect
	defer close(s.events)
	defer reader.Close()

	// One leading U+FEFF BYTE ORDER MARK character must be ignored if any are present.
	buffered := bufio.NewReader(reader)
	bom, err := buffered.Peek(len(utf8BOM))
	if err != nil {
		return err
	}
	if bytes.Equal(utf8BOM, bom) {
		if _, err := buffered.Discard(len(utf8BOM)); err != nil {
			return err
		}
	}

	// Event streams in this format must always be encoded as UTF-8.
	r := transform.NewReader(buffered, encoding.UTF8Validator)

	scanner := bufio.NewScanner(r)
	scanner.Split(splitLines)

	for scanner.Scan() {
		s.interpret(scanner.Bytes())
	}

	return scanner.Err()
}

func (s *Stream) interpret(line []byte) {
	switch {
	case len(line) == 0:
		// If the line is empty (a blank line)
		// Dispatch the event, as defined below.
		s.dispatch()
	case line[0] == ':':
		// If the line starts with a U+003A COLON character (:)
		// Ignore the line.
	default:
		// If the line contains a U+003A COLON character (:)
		field, value := line, []byte(nil)
		// Collect the characters on the line before the first U+003A COLON character (:), and let field be that string.
		// Collect the characters on the line after the first U+003A COLON character (:), and let value be that string.
		if split := bytes.SplitN(field, []byte{':'}, 2); len(split) == 2 {
			field = split[0]
			value = split[1]
			// If value starts with a U+0020 SPACE character, remove it from value.
			if value[0] == ' ' {
				value = value[1:]
			}
		}
		// Process the field using the steps described below, using field as the field name and value as the field value.

		// This otherwise is handled by the initial state of field and value.
		// Otherwise, the string is not empty but does not contain a U+003A COLON character (:)
		// Process the field using the steps described below, using the whole line as the field name, and the empty string as the field value.
		s.process(field, value)
	}
}

// https: //www.w3.org/TR/2015/REC-eventsource-20150203/#processField
func (s *Stream) process(name, value []byte) {
	// If the field name is "event"
	// Set the event type buffer to field value.
	if bytes.Equal(eventType, name) {
		s.eventType.Reset()
		s.eventType.Write(value)
		return
	}
	// If the field name is "data"
	// Append the field value to the data buffer, then append a single U+000A LINE FEED (LF) character to the data buffer.
	if bytes.Equal(dataType, name) {
		s.data.Write(value)
		s.data.WriteByte('\n')
		return
	}
	// If the field name is "id"
	// Set the last event ID buffer to the field value.
	if bytes.Equal(idType, name) {
		s.lastEventID.Reset()
		s.lastEventID.Write(value)
		return
	}
	// If the field name is "retry"
	// If the field value consists of only ASCII digits, then interpret the field value as an integer in base ten, and set the event stream's reconnection time to that integer. Otherwise, ignore the field.
	if bytes.Equal(retryType, name) {
		reconnectionTime, err := strconv.Atoi(string(value))
		if err == nil && reconnectionTime >= 0 {
			s.reconnectionTime = reconnectionTime
		}
		return
	}

	// Otherwise
	// The field is ignored.
}

// https://www.w3.org/TR/2015/REC-eventsource-20150203/#dispatchMessage
func (s Stream) dispatch() {
	// 1. Set the last event ID string of the event source to value of the last event ID buffer.
	// The buffer does not get reset, so the last event ID string of the event source remains set to this value until the next time it is set by the server.

	// 2. If the data buffer is an empty string, set the data buffer and the event type buffer to the empty string and abort these steps.
	if s.data.Len() == 0 {
		s.data.Reset()
		s.eventType.Reset()
		return
	}

	// 3. If the data buffer's last character is a U+000A LINE FEED (LF) character, then remove the last character from the data buffer.
	data := make([]byte, s.data.Len())
	copy(data, s.data.Bytes())
	if data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}

	// 4. Create an event that uses the MessageEvent interface, with the event type message, which does not bubble, is not cancelable, and has no default action.
	// The data attribute must be initialized to the value of the data buffer, the origin attribute must be initialized to the Unicode serialization
	// of the origin of the event stream's final URL (i.e. the URL after redirects), and the lastEventId attribute must be initialized
	// to the last event ID string of the event source. This event is not trusted.
	event := Event{
		Type: "message",
		Data: string(data),
	}

	// 5. If the event type buffer has a value other than the empty string, change the type of the newly created event to equal the value of the event type buffer.
	if s.eventType.Len() != 0 {
		event.Type = s.eventType.String()
	}

	// 6. Set the data buffer and the event type buffer to the empty string.
	s.data.Reset()
	s.eventType.Reset()

	// 7. Queue a task which, if the readyState attribute is set to a value other than CLOSED, dispatches the newly created event at the EventSource object.
	s.events <- event
}
