package worker

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func newTestOutput(t *testing.T) *Output {
	t.Helper()
	output, err := NewOutput(t.TempDir())
	if err != nil {
		t.Fatalf("New output Error %v", err)
	}
	t.Cleanup(func() {
		_ = output.file.Close()
	})

	return output
}

func readAllOutput(
	ctx context.Context,
	output *Output,
	offset int64,
) ([]byte, error) {
	var got []byte
	err := output.ReadFrom(ctx, offset, func(chunk []byte) error {
		got = append(got, chunk...)
		return nil
	})
	return got, err
}

func TestOutputReadFromReplayBinaryData(t *testing.T) {
	output := newTestOutput(t)
	want := []byte{0x00, 'h', 'i', 0xff}
	if err := output.Append(want); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	output.Close(nil)
	got, err := readAllOutput(context.Background(), output, 0)
	if err != nil {
		t.Fatalf("readfrom error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Read and want are different, got %v, want %v", got, want)
	}

}

func TestOutputReadFromUseOffset(t *testing.T) {
	output := newTestOutput(t)
	if err := output.Append([]byte("hello world")); err != nil {
		t.Fatalf("append failed error = %v", err)
	}
	output.Close(nil)
	got, err := readAllOutput(context.Background(), output, 6)
	if err != nil {
		t.Fatalf("Readfrom() error = %v", err)
	}
	if string(got) != "world" {
		t.Fatalf("Readfrom got %v but want %v", got, "world")
	}
}

func TestOutputAppendNotifyWaiters(t *testing.T) {
	output := newTestOutput(t)
	output.mu.Lock()
	notify := output.notify
	output.mu.Unlock()

	if err := output.Append([]byte("hello")); err != nil {
		t.Fatalf("append failed error = %v", err)
	}

	select {
	case <-notify:
		// receive signal
	default:
		t.Fatalf("waiter did not receive signal")
	}
}

func TestOutputWrite(t *testing.T) {
	output := newTestOutput(t)
	n, err := output.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("write failed %v", nil)
	}

	if n != len("hello") {
		t.Fatalf("Write not complete!")
	}
	output.Close(nil)
	got, err := readAllOutput(context.Background(), output, 0)
	if err != nil {
		t.Fatalf("Read failed %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("output %v is not equal to hello", got)
	}
}

func TestOutputAfterClose(t *testing.T) {
	output := newTestOutput(t)
	output.Close(nil)
	n, err := output.Write([]byte("later"))
	if !errors.Is(err, ErrOutputClosed) {
		t.Fatalf("Write error want ErrOutputClose, but get %v", err)
	}
	if n != 0 {
		t.Fatalf("unexpeceted write-through")
	}
}
