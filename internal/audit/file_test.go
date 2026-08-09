package audit

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestOpenAuditFileUsesRequiredFlagsAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	file, err := openAuditFile(path)
	if err != nil {
		t.Fatalf("open audit file: %v", err)
	}
	defer func() { _ = file.Close() }()

	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), syscall.F_GETFL, 0)
	if errno != 0 {
		t.Fatalf("read open flags: %v", errno)
	}
	if int(flags)&syscall.O_APPEND == 0 {
		t.Fatalf("open flags %#x omit O_APPEND", flags)
	}
	if int(flags)&syscall.O_ACCMODE != syscall.O_WRONLY {
		t.Fatalf("open access mode = %#x, want O_WRONLY", int(flags)&syscall.O_ACCMODE)
	}
	if int(flags)&syscall.O_NONBLOCK != 0 {
		t.Fatalf("open flags %#x retain O_NONBLOCK after the non-regular-file check", flags)
	}

	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat audit file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("audit file mode = %#o, want 0600", info.Mode().Perm())
	}
}

func TestFileSinkAppendsRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, []byte("existing\n"), 0o600); err != nil {
		t.Fatalf("seed audit file: %v", err)
	}
	sink, err := NewFileSink(path, nil)
	if err != nil {
		t.Fatalf("new file sink: %v", err)
	}
	if err := sink.Write([]byte("record\n")); err != nil {
		t.Fatalf("write record: %v", err)
	}
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("close sink: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	if string(got) != "existing\nrecord\n" {
		t.Fatalf("audit file = %q, want existing record followed by new record", got)
	}
}

func TestFileSinkRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.log")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatalf("create symlink target: %v", err)
	}
	link := filepath.Join(dir, "audit.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	if sink, err := NewFileSink(link, nil); err == nil {
		_ = sink.Close(context.Background())
		t.Fatal("file sink accepted a symlink")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if string(got) != "untouched" {
		t.Fatalf("symlink target changed to %q", got)
	}
}

func TestFileSinkRefusesFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create FIFO: %v", err)
	}

	type result struct {
		sink *FileSink
		err  error
	}
	opened := make(chan result, 1)
	go func() {
		sink, err := NewFileSink(path, nil)
		opened <- result{sink: sink, err: err}
	}()
	select {
	case outcome := <-opened:
		if outcome.err == nil {
			_ = outcome.sink.Close(context.Background())
			t.Fatal("file sink accepted a FIFO")
		}
	case <-time.After(time.Second):
		t.Fatal("opening a FIFO blocked instead of refusing it")
	}
}

func TestFileSinkRefusesDevice(t *testing.T) {
	info, err := os.Stat("/dev/null")
	if err != nil {
		t.Fatalf("stat real device /dev/null: %v", err)
	}
	if info.Mode()&os.ModeDevice == 0 {
		t.Fatalf("/dev/null mode %v is not a device; the device refusal oracle is unavailable", info.Mode())
	}
	if sink, err := NewFileSink("/dev/null", nil); err == nil {
		_ = sink.Close(context.Background())
		t.Fatal("file sink accepted a device")
	}
}

func TestFileSinkRefusesWorldWritableParent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("make parent world-writable: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	path := filepath.Join(dir, "audit.log")
	if sink, err := NewFileSink(path, nil); err == nil {
		_ = sink.Close(context.Background())
		t.Fatal("file sink accepted a world-writable parent")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused path was created: %v", err)
	}
}

func TestFileSinkReportsARealUnwritablePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("make parent unwritable: %v", err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	path := filepath.Join(dir, "audit.log")
	if sink, err := NewFileSink(path, nil); err == nil {
		_ = sink.Close(context.Background())
		t.Fatal("file sink opened a real unwritable path")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unwritable path was created: %v", err)
	}
}

type pipeShortWriter struct {
	fd    int
	once  sync.Once
	first chan int
}

func (w *pipeShortWriter) Write(p []byte) (int, error) {
	for {
		written, err := syscall.Write(w.fd, p)
		if errors.Is(err, syscall.EAGAIN) {
			time.Sleep(time.Millisecond)
			continue
		}
		w.once.Do(func() { w.first <- written })
		return written, err
	}
}

func TestWriteAllCompletesARealShortWrite(t *testing.T) {
	// os.File.Write retries internally on current Go, so it cannot prove this
	// loop. A direct syscall against a real nonblocking socket returns the
	// kernel's first short write before the peer starts draining.
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("create socket pair: %v", err)
	}
	reader := os.NewFile(uintptr(fds[0]), "audit-short-write-reader")
	writer := os.NewFile(uintptr(fds[1]), "audit-short-write-writer")
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	if err := syscall.SetNonblock(fds[1], true); err != nil {
		t.Fatalf("make socket nonblocking: %v", err)
	}

	payload := bytes.Repeat([]byte("audit-record\n"), 1<<17)
	shortWriter := &pipeShortWriter{fd: fds[1], first: make(chan int, 1)}
	written := make(chan error, 1)
	go func() { written <- writeAll(shortWriter, payload) }()

	first := <-shortWriter.first
	if first <= 0 || first >= len(payload) {
		t.Fatalf("real socket first write = %d of %d bytes; setup did not produce a short write", first, len(payload))
	}

	read := make(chan []byte, 1)
	go func() {
		got, _ := io.ReadAll(reader)
		read <- got
	}()
	if err := <-written; err != nil {
		t.Fatalf("complete short write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close socket writer: %v", err)
	}
	if got := <-read; !bytes.Equal(got, payload) {
		t.Fatalf("short write delivered %d bytes, want %d", len(got), len(payload))
	}
}

func TestFileSinkReopenAfterRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	rotated := filepath.Join(dir, "audit.log.1")
	sink, err := NewFileSink(path, nil)
	if err != nil {
		t.Fatalf("new file sink: %v", err)
	}
	if err := sink.Write([]byte("before\n")); err != nil {
		t.Fatalf("write before rename: %v", err)
	}
	if err := os.Rename(path, rotated); err != nil {
		t.Fatalf("rename audit file: %v", err)
	}
	if err := sink.Reopen(); err != nil {
		t.Fatalf("reopen audit file: %v", err)
	}
	if err := sink.Write([]byte("after\n")); err != nil {
		t.Fatalf("write after reopen: %v", err)
	}
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("close sink: %v", err)
	}

	oldRecord, err := os.ReadFile(rotated)
	if err != nil {
		t.Fatalf("read rotated file: %v", err)
	}
	newRecord, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read reopened file: %v", err)
	}
	if string(oldRecord) != "before\n" || string(newRecord) != "after\n" {
		t.Fatalf("rotated = %q, reopened = %q; Reopen did not switch descriptors in order", oldRecord, newRecord)
	}
}

func TestFileSinkMeasurementUsesOpenDescriptorAfterRename(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "active")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create active audit directory: %v", err)
	}
	path := filepath.Join(dir, "audit.log")
	rotatedDir := filepath.Join(root, "rotated")
	sink, err := NewFileSink(path, nil)
	if err != nil {
		t.Fatalf("new file sink: %v", err)
	}
	defer func() { _ = sink.Close(context.Background()) }()

	record := []byte("still-open descriptor\n")
	if err := sink.Write(record); err != nil {
		t.Fatalf("write before rename: %v", err)
	}
	if err := os.Rename(dir, rotatedDir); err != nil {
		t.Fatalf("rename audit directory: %v", err)
	}
	measurement, err := sink.Measure()
	if err != nil {
		t.Fatalf("measure open descriptor while configured path is absent: %v", err)
	}
	if measurement.FilesystemFreeBytes <= 0 {
		t.Fatalf("descriptor filesystem free bytes = %d, want available space", measurement.FilesystemFreeBytes)
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(rotatedDir, &filesystem); err != nil {
		t.Fatalf("read renamed file filesystem oracle: %v", err)
	}
	wantFree := int64(uint64(filesystem.Bavail) * uint64(filesystem.Bsize))
	difference := measurement.FilesystemFreeBytes - wantFree
	if difference < 0 {
		difference = -difference
	}
	if difference > 16*1024*1024 {
		t.Fatalf(
			"descriptor filesystem free bytes = %d, statfs oracle = %d",
			measurement.FilesystemFreeBytes, wantFree,
		)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create replacement audit directory: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create empty replacement path: %v", err)
	}

	measurement, err = sink.Measure()
	if err != nil {
		t.Fatalf("measure renamed open descriptor: %v", err)
	}
	if measurement.CurrentFileSizeBytes != int64(len(record)) {
		t.Fatalf(
			"current open file size = %d, want rotated descriptor size %d; replacement path is empty",
			measurement.CurrentFileSizeBytes, len(record),
		)
	}
	if measurement.FilesystemFreeBytes <= 0 {
		t.Fatalf("filesystem free bytes = %d, want a real non-empty test filesystem", measurement.FilesystemFreeBytes)
	}
}

func TestFileSinkMeasurementReportsDescriptorStatFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	file, err := openAuditFile(path)
	if err != nil {
		t.Fatalf("open audit file: %v", err)
	}
	sink := newFileSink(path, file, time.Second, 1, nil)
	if err := file.Close(); err != nil {
		t.Fatalf("close descriptor underneath worker: %v", err)
	}
	if measurement, err := sink.Measure(); err == nil {
		t.Fatalf("measurement of closed descriptor = %+v, want an unavailable measurement error", measurement)
	}
	_ = sink.Close(context.Background())
}

func TestFileSinkCommandsWaitBehindBlockedWrite(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	defer func() { _ = reader.Close() }()
	path := filepath.Join(t.TempDir(), "reopened.log")
	sink := newFileSink(path, writer, 20*time.Millisecond, 4, nil)

	before := bytes.Repeat([]byte("b"), 1<<20)
	if err := sink.Write(before); !errors.Is(err, ErrSinkTimeout) {
		t.Fatalf("blocked write = %v, want ErrSinkTimeout", err)
	}
	if measurement, err := sink.Measure(); !errors.Is(err, ErrSinkTimeout) {
		t.Fatalf("measurement queued behind blocked write = %+v, %v; want ErrSinkTimeout", measurement, err)
	}
	if err := sink.Reopen(); !errors.Is(err, ErrSinkTimeout) {
		t.Fatalf("reopen queued behind blocked write = %v, want ErrSinkTimeout", err)
	}
	if err := sink.Write([]byte("after")); !errors.Is(err, ErrSinkTimeout) {
		t.Fatalf("write queued behind reopen = %v, want ErrSinkTimeout", err)
	}

	oldRead := make(chan []byte, 1)
	go func() {
		got, _ := io.ReadAll(reader)
		oldRead <- got
	}()
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("close sink: %v", err)
	}
	if got := <-oldRead; !bytes.Equal(got, before) {
		t.Fatalf("old descriptor received %d bytes, want the complete %d-byte first record", len(got), len(before))
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read reopened file: %v", err)
	}
	if string(after) != "after" {
		t.Fatalf("reopened file = %q, want only the command queued after Reopen", after)
	}
}

func TestFileSinkFailedReopenKeepsSafeDescriptor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	rotated := filepath.Join(dir, "audit.log.1")
	unsafeTarget := filepath.Join(dir, "unsafe.log")
	if err := os.WriteFile(unsafeTarget, []byte("untouched"), 0o600); err != nil {
		t.Fatalf("create unsafe target: %v", err)
	}
	sink, err := NewFileSink(path, nil)
	if err != nil {
		t.Fatalf("new file sink: %v", err)
	}
	if err := sink.Write([]byte("before\n")); err != nil {
		t.Fatalf("write before rename: %v", err)
	}
	if err := os.Rename(path, rotated); err != nil {
		t.Fatalf("rename audit file: %v", err)
	}
	if err := os.Symlink(unsafeTarget, path); err != nil {
		t.Fatalf("replace path with symlink: %v", err)
	}
	if err := sink.Reopen(); err == nil {
		t.Fatal("Reopen accepted a symlink replacement")
	}
	if err := sink.Write([]byte("after\n")); err != nil {
		t.Fatalf("write through retained descriptor: %v", err)
	}
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("close sink: %v", err)
	}

	got, err := os.ReadFile(rotated)
	if err != nil {
		t.Fatalf("read retained file: %v", err)
	}
	if string(got) != "before\nafter\n" {
		t.Fatalf("retained file = %q, want both records", got)
	}
	got, err = os.ReadFile(unsafeTarget)
	if err != nil {
		t.Fatalf("read unsafe target: %v", err)
	}
	if string(got) != "untouched" {
		t.Fatalf("unsafe target changed to %q", got)
	}
}

func TestFileSinkTimedOutWriteLandsInOrderAndCloseDrains(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	defer func() { _ = reader.Close() }()
	sink := newFileSink("real blocked pipe", writer, 20*time.Millisecond, 4, nil)

	wantFirst := bytes.Repeat([]byte("a"), 1<<20)
	first := append([]byte(nil), wantFirst...)
	if err := sink.Write(first); !errors.Is(err, ErrSinkTimeout) {
		t.Fatalf("blocked write = %v, want ErrSinkTimeout", err)
	}
	first[len(first)-1] = 'x'
	second := []byte("second")
	if err := sink.Write(second); !errors.Is(err, ErrSinkTimeout) {
		t.Fatalf("queued write = %v, want ErrSinkTimeout", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- sink.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned before blocked and queued writes drained: %v", err)
	case <-time.After(40 * time.Millisecond):
	}

	read := make(chan []byte, 1)
	go func() {
		got, _ := io.ReadAll(reader)
		read <- got
	}()
	if err := <-closed; err != nil {
		t.Fatalf("close drained sink: %v", err)
	}
	want := append(append([]byte(nil), wantFirst...), second...)
	if got := <-read; !bytes.Equal(got, want) {
		t.Fatalf("drained record contents or order differ (got %d bytes, want %d)", len(got), len(want))
	}
	if err := sink.Write([]byte("late")); !errors.Is(err, ErrSinkClosed) {
		t.Fatalf("write after Close = %v, want ErrSinkClosed", err)
	}
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestFileSinkFullQueueFailsImmediately(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	defer func() { _ = reader.Close() }()
	sink := newFileSink("real blocked pipe", writer, 20*time.Millisecond, 1, nil)

	first := bytes.Repeat([]byte("a"), 1<<20)
	if err := sink.Write(first); !errors.Is(err, ErrSinkTimeout) {
		t.Fatalf("blocked write = %v, want ErrSinkTimeout", err)
	}
	if err := sink.Write([]byte("queued")); !errors.Is(err, ErrSinkTimeout) {
		t.Fatalf("queued write = %v, want ErrSinkTimeout", err)
	}
	started := time.Now()
	if err := sink.Write([]byte("rejected")); !errors.Is(err, ErrSinkQueueFull) {
		t.Fatalf("write to full queue = %v, want ErrSinkQueueFull", err)
	}
	if elapsed := time.Since(started); elapsed >= 20*time.Millisecond {
		t.Fatalf("full queue failed after %v, want immediate refusal before the write timeout", elapsed)
	}

	go func() { _, _ = io.Copy(io.Discard, reader) }()
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("close sink: %v", err)
	}
}

func TestFileSinkBoundedShutdownProcessNamesAbandonedRecords(t *testing.T) {
	if os.Getenv("DUFFLEBAG_BLOCKED_SINK_HELPER") == "1" {
		runBlockedSinkHelper(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestFileSinkBoundedShutdownProcessNamesAbandonedRecords$")
	command.Env = append(os.Environ(), "DUFFLEBAG_BLOCKED_SINK_HELPER=1", "GORACE=atexit_sleep_ms=0")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("capture blocked process output: %v", err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatalf("start blocked process: %v", err)
	}
	// Wait is only ever called after every pipe read completes: Wait closes
	// the stdout pipe when the child exits, so calling it concurrently with
	// reads races the close against the final read on a loaded machine.
	kill := true
	defer func() {
		if kill {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	reader := bufio.NewReader(stdout)
	ready, err := reader.ReadString('\n')
	if err != nil || ready != "READY\n" {
		t.Fatalf("blocked process readiness = %q, %v", ready, err)
	}
	var output bytes.Buffer
	copied := make(chan error, 1)
	go func() { _, err := io.Copy(&output, reader); copied <- err }()

	started := time.Now()
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal blocked process: %v", err)
	}
	// EOF on the pipe is the child exiting; only then is Wait safe to call.
	select {
	case err := <-copied:
		if err != nil {
			t.Fatalf("read blocked process output: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked audit process did not exit within 1s shutdown bound")
	}
	kill = false
	if err := command.Wait(); err != nil {
		t.Fatalf("blocked process exit: %v; output: %s", err, output.String())
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("blocked audit process exit took %v, want under 1s", elapsed)
	}
	got := output.String()
	if !strings.Contains(got, `"msg":"audit sink shutdown abandoned records"`) ||
		!strings.Contains(got, `"count":2`) ||
		!strings.Contains(got, `"correlation_ids":["blocked-response","queued-response"]`) {
		t.Fatalf("shutdown WARN did not name 2 abandoned records and their correlation ids: %s", got)
	}
}

func runBlockedSinkHelper(t *testing.T) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create permanently blocked descriptor: %v", err)
	}
	defer func() { _ = reader.Close() }()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	sink := newFileSink("permanently blocked descriptor", writer, 20*time.Millisecond, 4, logger)
	blocked := fmt.Appendf(nil, `{"correlation_id":"blocked-response","padding":"%s"}`, bytes.Repeat([]byte("x"), 1<<20))
	if err := sink.Write(blocked); !errors.Is(err, ErrSinkTimeout) {
		t.Fatalf("prime blocked descriptor: %v", err)
	}
	if err := sink.Write([]byte(`{"correlation_id":"queued-response"}`)); !errors.Is(err, ErrSinkTimeout) {
		t.Fatalf("queue second record: %v", err)
	}

	terminated := make(chan os.Signal, 1)
	signal.Notify(terminated, syscall.SIGTERM)
	defer signal.Stop(terminated)
	fmt.Println("READY")
	<-terminated
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := sink.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded close = %v, want context deadline exceeded", err)
	}
}
