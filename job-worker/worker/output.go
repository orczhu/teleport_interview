package worker

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

const outputChunkSize = 32 * 1024

var (
	ErrOutputClosed        = errors.New("output is closed")
	ErrInvalidOutputOffset = errors.New("onput offset less than 0")
	ErrNilOutputWriter     = errors.New("output writer is nil")
)

type Output struct {
	mu     sync.Mutex
	file   *os.File
	size   int64
	notify chan struct{}
	closed bool
	err    error
}

// Append bytes → advance size → close current notify channel → create replacement
// reader      → read bytes from its private offset → wait on notify or ctx.Done
// Close       → mark closed → notify every waiting reader

func NewOutput(dir string) (*Output, error) {
	file, err := os.CreateTemp(dir, "job-output-*")
	if err != nil {
		return nil, err
	}

	return &Output{
		file:   file,
		notify: make(chan struct{}),
	}, nil
}

func (o *Output) Append(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return ErrOutputClosed
	}
	n, err := o.file.Write(data)
	if n > 0 {
		o.size += int64(n)
		o.notifyReadersLocked()
	}
	if err != nil {
		return err
	}
	// does not have full data
	if n != len(data) {
		return io.ErrShortWrite
	}

	return nil

}

func (o *Output) notifyReadersLocked() {
	close(o.notify)
	o.notify = make(chan struct{})

}

func (o *Output) Close(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.closed = true
	o.err = err
	o.notifyReadersLocked()
}

func (o *Output) Write(data []byte) (int, error) {
	if err := o.Append(data); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (o *Output) ReadFrom(ctx context.Context, offset int64, write func([]byte) error) error {
	if offset < 0 {
		return ErrInvalidOutputOffset
	}

	if write == nil {
		return ErrNilOutputWriter
	}

	for {
		o.mu.Lock()
		if offset >= o.size {
			if o.closed {
				err := o.err
				o.mu.Unlock()
				return err
			}

			notify := o.notify
			o.mu.Unlock()

			select {
			case <-notify:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		end := o.size
		o.mu.Unlock()

		for offset < end {
			chunkSize := outputChunkSize
			if remaining := end - offset; remaining < int64(chunkSize) {
				chunkSize = int(remaining)
			}
			chunk := make([]byte, chunkSize)
			n, err := o.file.ReadAt(chunk, offset)
			if n > 0 {
				if err := write(chunk[:n]); err != nil {
					return err
				}
				offset += int64(n)
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}

			if n == 0 {
				return io.ErrUnexpectedEOF
			}
		}

	}
}
