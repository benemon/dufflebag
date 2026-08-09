package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

const (
	fileSinkQueueDepth   = 64
	fileSinkWriteTimeout = 2 * time.Second
)

var (
	// ErrNotRegularFile identifies a path that opened but is unsafe as an append-only file target.
	ErrNotRegularFile = errors.New("audit target is not a regular file")
	// ErrWorldWritableParent identifies a path whose parent permits unsafe replacement.
	ErrWorldWritableParent = errors.New("audit target parent is world-writable")
)

type fileCommandKind uint8

const (
	fileWrite fileCommandKind = iota
	fileReopen
	fileMeasure
	fileClose
)

type fileMeasurementResult struct {
	measurement SinkMeasurement
	err         error
}

type fileCommand struct {
	kind        fileCommandKind
	record      []byte
	id          uint64
	done        chan error
	measurement chan fileMeasurementResult
}

// FileSink keeps descriptor access behind one worker so timed-out writes cannot
// race a reopen or close while they finish late.
type FileSink struct {
	path         string
	commands     chan fileCommand
	writeTimeout time.Duration
	logger       *slog.Logger

	mu      sync.Mutex
	closing bool
	closed  chan struct{}
	// pending contains commands accepted by Write but not completed by the
	// descriptor owner. It is the shutdown-loss inventory.
	pending  map[uint64]string
	nextID   uint64
	closeErr error
}

func NewFileSink(path string, logger *slog.Logger) (*FileSink, error) {
	file, err := openAuditFile(path)
	if err != nil {
		return nil, err
	}
	return newFileSink(path, file, fileSinkWriteTimeout, fileSinkQueueDepth, logger), nil
}

func newFileSink(
	path string,
	file *os.File,
	writeTimeout time.Duration,
	queueDepth int,
	logger *slog.Logger,
) *FileSink {
	if logger == nil {
		logger = slog.Default()
	}
	sink := &FileSink{
		path:         path,
		commands:     make(chan fileCommand, queueDepth),
		writeTimeout: writeTimeout,
		logger:       logger,
		closed:       make(chan struct{}),
		pending:      make(map[uint64]string),
	}
	go sink.run(file)
	return sink
}

func (s *FileSink) Write(record []byte) error {
	// A timed-out command remains owned by the worker, so it must not retain
	// memory the caller can change while that late write is still pending.
	command := fileCommand{
		kind:   fileWrite,
		record: append([]byte(nil), record...),
		done:   make(chan error, 1),
	}
	if err := s.submitWrite(command); err != nil {
		return fmt.Errorf("write audit file %q: %w", s.path, err)
	}
	return nil
}

func (s *FileSink) submitWrite(command fileCommand) error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return ErrSinkClosed
	}
	s.nextID++
	command.id = s.nextID
	s.pending[command.id] = recordCorrelationID(command.record)
	select {
	case s.commands <- command:
		s.mu.Unlock()
	default:
		delete(s.pending, command.id)
		s.mu.Unlock()
		return ErrSinkQueueFull
	}

	timer := time.NewTimer(s.writeTimeout)
	defer timer.Stop()
	select {
	case err := <-command.done:
		return err
	case <-timer.C:
		return ErrSinkTimeout
	}
}

func (s *FileSink) Reopen() error {
	command := fileCommand{kind: fileReopen, done: make(chan error, 1)}
	if err := s.submit(command); err != nil {
		return fmt.Errorf("reopen audit file %q: %w", s.path, err)
	}
	return nil
}

// Measure inspects the descriptor owned by the worker. The configured path may
// name a different file between log rotation and Reopen.
func (s *FileSink) Measure() (SinkMeasurement, error) {
	command := fileCommand{
		kind: fileMeasure, measurement: make(chan fileMeasurementResult, 1),
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return SinkMeasurement{}, ErrSinkClosed
	}
	select {
	case s.commands <- command:
		s.mu.Unlock()
	default:
		s.mu.Unlock()
		return SinkMeasurement{}, ErrSinkQueueFull
	}

	timer := time.NewTimer(s.writeTimeout)
	defer timer.Stop()
	select {
	case result := <-command.measurement:
		return result.measurement, result.err
	case <-timer.C:
		return SinkMeasurement{}, ErrSinkTimeout
	}
}

func (s *FileSink) submit(command fileCommand) error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return ErrSinkClosed
	}
	select {
	case s.commands <- command:
		s.mu.Unlock()
	default:
		s.mu.Unlock()
		return ErrSinkQueueFull
	}

	timer := time.NewTimer(s.writeTimeout)
	defer timer.Stop()
	select {
	case err := <-command.done:
		return err
	case <-timer.C:
		return ErrSinkTimeout
	}
}

func (s *FileSink) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closing {
		closed := s.closed
		s.mu.Unlock()
		select {
		case <-closed:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.mu.Lock()
		err := s.closeErr
		s.mu.Unlock()
		return err
	}
	s.closing = true
	s.mu.Unlock()

	// Writes that won the enqueue race are ahead of this command. The process's
	// shutdown deadline bounds both waiting for queue space and the drain.
	command := fileCommand{kind: fileClose, done: make(chan error, 1)}
	select {
	case s.commands <- command:
	case <-ctx.Done():
		return s.finishClose(ctx.Err())
	}
	var err error
	select {
	case err = <-command.done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	return s.finishClose(err)
}

func (s *FileSink) finishClose(err error) error {
	s.mu.Lock()
	if err != nil && len(s.pending) != 0 {
		ids := make([]uint64, 0, len(s.pending))
		for id := range s.pending {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		correlations := make([]string, 0, len(ids))
		for _, id := range ids {
			correlations = append(correlations, s.pending[id])
		}
		s.logger.Warn(
			"audit sink shutdown abandoned records",
			"path", s.path,
			"count", len(ids),
			"correlation_ids", correlations,
		)
	}
	s.closeErr = err
	close(s.closed)
	s.mu.Unlock()
	return err
}

func recordCorrelationID(record []byte) string {
	var fields struct {
		CorrelationID string `json:"correlation_id"`
	}
	if err := json.Unmarshal(record, &fields); err != nil || fields.CorrelationID == "" {
		return "unknown"
	}
	return fields.CorrelationID
}

func (s *FileSink) run(file *os.File) {
	for command := range s.commands {
		switch command.kind {
		case fileWrite:
			err := writeAll(file, command.record)
			s.mu.Lock()
			delete(s.pending, command.id)
			s.mu.Unlock()
			command.done <- err
		case fileReopen:
			replacement, err := openAuditFile(s.path)
			if err == nil {
				old := file
				file = replacement
				err = old.Close()
			}
			command.done <- err
		case fileMeasure:
			info, err := file.Stat()
			if err != nil {
				command.measurement <- fileMeasurementResult{err: err}
				continue
			}
			var filesystem syscall.Statfs_t
			if err := syscall.Fstatfs(int(file.Fd()), &filesystem); err != nil {
				command.measurement <- fileMeasurementResult{err: err}
				continue
			}
			blocks := uint64(filesystem.Bavail)
			blockSize := uint64(filesystem.Bsize)
			const maxInt64 = uint64(1<<63 - 1)
			if blockSize != 0 && blocks > maxInt64/blockSize {
				command.measurement <- fileMeasurementResult{
					err: errors.New("audit target filesystem free space exceeds int64"),
				}
				continue
			}
			command.measurement <- fileMeasurementResult{measurement: SinkMeasurement{
				CurrentFileSizeBytes: info.Size(),
				FilesystemFreeBytes:  int64(blocks * blockSize),
			}}
		case fileClose:
			command.done <- file.Close()
			return
		}
	}
}

func writeAll(writer io.Writer, record []byte) error {
	for len(record) > 0 {
		written, err := writer.Write(record)
		if written > 0 {
			record = record[written:]
		}
		if err != nil && (!errors.Is(err, io.ErrShortWrite) || written == 0) {
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

func openAuditFile(path string) (*os.File, error) {
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("inspect audit file parent: %w", err)
	}
	if parent.Mode().Perm()&0o002 != 0 {
		return nil, fmt.Errorf("%w: %q", ErrWorldWritableParent, filepath.Dir(path))
	}

	// O_NOFOLLOW refuses symlinks without an lstat/open race. O_NONBLOCK makes
	// a FIFO rejectable by fstat instead of letting its open hang startup.
	file, err := os.OpenFile(
		path,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("open audit file: %w", err)
	}
	closeOnError := func(openErr error) (*os.File, error) {
		if closeErr := file.Close(); closeErr != nil {
			return nil, errors.Join(openErr, closeErr)
		}
		return nil, openErr
	}

	opened, err := file.Stat()
	if err != nil {
		return closeOnError(fmt.Errorf("inspect opened audit file: %w", err))
	}
	if !opened.Mode().IsRegular() {
		return closeOnError(fmt.Errorf("%w: %q", ErrNotRegularFile, path))
	}
	if err := syscall.SetNonblock(int(file.Fd()), false); err != nil {
		return closeOnError(fmt.Errorf("make audit file blocking: %w", err))
	}
	return file, nil
}
