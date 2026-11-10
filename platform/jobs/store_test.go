package jobs

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/plinth-go/plinth/internal/jsonx"
)

type typedNilJobError struct{}

func (*typedNilJobError) Error() string { panic("typed nil job error was dereferenced") }

type duplicateKeyPayload struct{}

const (
	testJobID      = "018f0000-0000-7000-8000-000000000000"
	testLeaseToken = "018f0000-0000-7000-8000-000000000001"
)

func (duplicateKeyPayload) MarshalJSON() ([]byte, error) {
	return []byte(`{"name":"first","name":"second"}`), nil
}

func TestMarshalPayloadNormalizesNullToEmptyObject(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	var typedNil *payload
	var nilMap map[string]string

	tests := []struct {
		name    string
		payload any
		want    string
	}{
		{name: "nil interface", payload: nil, want: `{}`},
		{name: "typed nil pointer", payload: typedNil, want: `{}`},
		{name: "nil map", payload: nilMap, want: `{}`},
		{name: "raw null", payload: json.RawMessage("null"), want: `{}`},
		{name: "whitespace raw null", payload: json.RawMessage("  null  "), want: `{}`},
		{name: "value", payload: payload{Name: "work"}, want: `{"name":"work"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := marshalPayload(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("payload = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMarshalPayloadRejectsUnsupportedValues(t *testing.T) {
	if _, err := marshalPayload(make(chan int)); err == nil {
		t.Fatal("unsupported payload was accepted")
	}
}

func TestMarshalPayloadRejectsValuesTypedHandlersCannotDecode(t *testing.T) {
	type nullablePayload struct {
		Value *string `json:"value"`
	}
	if _, err := marshalPayload(nullablePayload{}); !errors.Is(err, jsonx.ErrNull) {
		t.Fatalf("nested null error = %v", err)
	}
	if _, err := marshalPayload(duplicateKeyPayload{}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate member error = %v", err)
	}
}

func TestErrorMessageIsBoundedAndUTF8Safe(t *testing.T) {
	if got := errorMessage(errors.New("boom")); got != "boom" {
		t.Fatalf("short message = %q", got)
	}
	if got := errorMessage(errors.New(string([]byte{0xff, 0xfe, 'x'}))); !utf8.ValidString(got) {
		t.Fatalf("short invalid UTF-8 message = %q", got)
	}
	message := strings.Repeat("错", maxJobErrorBytes)
	got := errorMessage(errors.New(message))
	if len(got) > maxJobErrorBytes {
		t.Fatalf("truncated message has %d bytes, want at most %d", len(got), maxJobErrorBytes)
	}
	if !strings.HasSuffix(got, truncatedJobErrorSuffix) {
		t.Fatalf("truncated message = %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncated message is not valid UTF-8")
	}
	invalid := strings.Repeat("\xff", maxJobErrorBytes*2)
	got = errorMessage(errors.New(invalid))
	if len(got) > maxJobErrorBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, truncatedJobErrorSuffix) {
		t.Fatalf("invalid UTF-8 message = %q", got)
	}
}

func TestStoreCanonicalizesIdentifiers(t *testing.T) {
	id, err := normalizeJobID("  {F81D4FAE-7DEC-11D0-A765-00A0C91E6BF6}  ")
	if err != nil {
		t.Fatal(err)
	}
	if id != "f81d4fae-7dec-11d0-a765-00a0c91e6bf6" {
		t.Fatalf("job ID = %q", id)
	}
	workerID, err := normalizeWorkerID("  worker-1  ")
	if err != nil {
		t.Fatal(err)
	}
	if workerID != "worker-1" {
		t.Fatalf("worker ID = %q", workerID)
	}
	lease, err := normalizeLease(Lease{
		JobID: " " + id + " ", WorkerID: " " + workerID + " ", Attempt: 2,
		Token: " {" + testLeaseToken + "} ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.JobID != id || lease.WorkerID != workerID || lease.Attempt != 2 || lease.Token != testLeaseToken {
		t.Fatalf("lease = %#v", lease)
	}
}

func TestResolveEnqueueOptionsValidatesWithoutABackend(t *testing.T) {
	options, err := ResolveEnqueueOptions()
	if err != nil {
		t.Fatal(err)
	}
	if options.MaxAttempts != 3 {
		t.Fatalf("default max attempts = %d", options.MaxAttempts)
	}
	if _, err := ResolveEnqueueOptions(WithMaxAttempts(0)); err == nil || !strings.Contains(err.Error(), "at least 1") {
		t.Fatalf("zero attempts error = %v", err)
	}
	if _, err := ResolveEnqueueOptions(nil); err == nil || !strings.Contains(err.Error(), "option at index 0 is nil") {
		t.Fatalf("nil option error = %v", err)
	}
}

func TestNormalizeScheduleNamesCanonicalizesAndRejectsAmbiguity(t *testing.T) {
	names, err := normalizeScheduleNames([]string{" first ", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "first,second" {
		t.Fatalf("names = %#v", names)
	}
	if _, err := normalizeScheduleNames([]string{"first", " first "}); err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := normalizeScheduleNames([]string{"  "}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty error = %v", err)
	}
}

func TestIdentifiersRejectInvalidUTF8ControlAndOversize(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	tooLong := strings.Repeat("x", maxIdentifierBytes+1)
	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "job kind invalid UTF-8",
			call: func() error { _, err := normalizeJobKind(invalidUTF8); return err },
			want: "valid UTF-8",
		},
		{
			name: "job kind control character",
			call: func() error { _, err := normalizeJobKind("task\nkind"); return err },
			want: "printable",
		},
		{
			name: "job kind too long",
			call: func() error { _, err := normalizeJobKind(tooLong); return err },
			want: "must not exceed",
		},
		{
			name: "worker ID invalid UTF-8",
			call: func() error { _, err := normalizeWorkerID(invalidUTF8); return err },
			want: "valid UTF-8",
		},
		{
			name: "schedule name control character",
			call: func() error {
				return validateIdentifier("daily\nreport", "schedule name")
			},
			want: "printable",
		},
		{
			name: "schedule kind too long",
			call: func() error {
				_, err := normalizeSchedule(Schedule{Name: "valid", Kind: tooLong, Interval: time.Minute})
				return err
			},
			want: "must not exceed",
		},
		{
			name: "disabled schedule name too long",
			call: func() error { _, err := normalizeScheduleNames([]string{tooLong}); return err },
			want: "must not exceed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
