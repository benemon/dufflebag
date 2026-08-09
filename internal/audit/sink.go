package audit

import (
	"context"
	"errors"
)

var (
	ErrSinkClosed    = errors.New("audit sink is closed")
	ErrSinkQueueFull = errors.New("audit sink queue is full")
	ErrSinkTimeout   = errors.New("audit sink command timed out")
)

// Sink is one independently writable audit destination.
type Sink interface {
	Write([]byte) error
	Reopen() error
	Measure() (SinkMeasurement, error)
	Close(context.Context) error
}

// SinkMeasurement describes the open file and the filesystem that contains it.
type SinkMeasurement struct {
	CurrentFileSizeBytes int64
	FilesystemFreeBytes  int64
}
