package sse

import (
	"bytes"
	"io/ioutil"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStream(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	type testCase struct {
		name           string
		input          string
		expectedEvents []Event
	}
	testCases := []testCase{
		{
			name: "test",
			input: `event: score
data: {"exam": 3, "studentId": "foo", "score": .991}

`,
			expectedEvents: []Event{
				{
					Type: "score",
					Data: `{"exam": 3, "studentId": "foo", "score": .991}`,
				},
			},
		},
		{
			name: "stock quote example",
			input: `data: YHOO
data: +2
data: 10

`,
			expectedEvents: []Event{
				{
					Type: "message",
					Data: "YHOO\n+2\n10",
				},
			},
		},
		{
			name: "example with id",
			input: `: test stream

data: first event
id: 1

data:second event
id

data:  third event

`,
			expectedEvents: []Event{
				{
					Type: "message",
					Data: "first event",
				},
				{
					Type: "message",
					Data: "second event",
				},
				{
					Type: "message",
					Data: " third event",
				},
			},
		},
		{
			name: "empty data",
			input: `
data

data
data

data:`,
			expectedEvents: []Event{
				{
					Type: "message",
					Data: "",
				},
				{
					Type: "message",
					Data: "\n",
				},
			},
		},
		{
			name: "leading space is removed",
			input: `data:test

data: test

`,
			expectedEvents: []Event{
				{
					Type: "message",
					Data: "test",
				},
				{
					Type: "message",
					Data: "test",
				},
			},
		},
		{
			name:  "utf-8 BOM is ignored",
			input: "\xfe\xffdata: foo\n\n",
			expectedEvents: []Event{
				{
					Type: "message",
					Data: "foo",
				},
			},
		},
	}

	runTestCase := func(tc testCase) func(*testing.T) {
		return func(t *testing.T) {
			r := ioutil.NopCloser(strings.NewReader(tc.input))
			s := Stream{
				events:      make(chan Event, len(tc.expectedEvents)),
				data:        new(bytes.Buffer),
				eventType:   new(bytes.Buffer),
				lastEventID: new(bytes.Buffer),
			}
			require.NoError(s.parse(r))

			var actualEvents []Event
			for event := range s.events {
				actualEvents = append(actualEvents, event)
			}

			assert.Equal(tc.expectedEvents, actualEvents)
		}
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, runTestCase(testCase))
	}
}

func TestStreamInvalidUTF8(t *testing.T) {
	r := ioutil.NopCloser(strings.NewReader("\x80"))
	s := Stream{events: make(chan Event)}
	assert.Error(t, s.parse(r))
}
