package flushsyncwriter

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
)

// bufferSize sizes the buffer associated with each log file. It's large
// so that log records can accumulate without the logging thread blocking
// on disk I/O. The flushDaemon will block instead.
const bufferSize = 256 * 1024

// ensure syncBuffer implements flushSyncWriter
var _ FlushSyncWriter = &syncBuffer{}

// MaxSize is the maximum size of a log file in bytes. A log file will
// rotate when the file size exceeds the MaxSize.
var MaxSize uint64 = 1024 * 1024 * 10

// MaxNumber is the maximum number of file to keep in one run. Once exceeds,
// The oldest file will be removed before rotating
var MaxNumber uint = 100

// If non-empty, write stat files in this directory.
// Otherwise, write in current directory.
var LogDir string = ""

// onceLogDirs helps to create the directory only once if it does not exist.
var onceLogDirs sync.Once

// These variables can be used to uniquely identify a log file name.
var (
	pid      = os.Getpid()
	program  = filepath.Base(os.Args[0])
	host     = "unknownhost"
	userName = "unknownuser"
)

// flushSyncWriter is the interface satisfied by logging destinations.
type FlushSyncWriter interface {
	Flush() error
	Sync() error
	io.Writer
}

// syncBuffer is the implement of flushSyncWriter
type syncBuffer struct {
	*bufio.Writer // Buffering
	file   *os.File // Output to file
	nbytes uint64 // The number of bytes written to the file
}

func init() {
	h, err := os.Hostname()
	if err == nil {
		host = shortHostname(h)
	}

	current, err := user.Current()
	if err == nil {
		userName = current.Username
	}

	// Sanitize userName since it may contain filepath separators on Windows.
	userName = strings.Replace(userName, `\`, "_", -1)
}

func (sb *syncBuffer) Sync() error {
	return sb.file.Sync()
}

func (sb *syncBuffer) Write(p []byte) (n int, err error) {
	if sb.nbytes+uint64(len(p)) >= MaxSize {
		if err := sb.rotateFile(time.Now()); err != nil {
			glog.Fatal(err)
		}
	}
	n, err = sb.Writer.Write(p)
	sb.nbytes += uint64(n)
	if err != nil {
		glog.Fatal(err)
	}
	return
}

// rotateFile closes the syncBuffer's file and starts a new one.
func (sb *syncBuffer) rotateFile(now time.Time) error {
	if sb.file != nil {
		sb.Flush()
		sb.file.Close()
	}

	if rotated := rotatedFileNames(); len(rotated) > int(MaxNumber) {
		removes := rotated[0 : len(rotated) - int(MaxNumber)]
		for _, name := range removes {
			os.Remove(filepath.Join(LogDir, name))
		}
	}

	var err error
	sb.file, _, err = createFile(now)
	sb.nbytes = 0
	if err != nil {
		return err
	}
	sb.Writer = bufio.NewWriterSize(sb.file, bufferSize)

	// Write header.
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Log file created at: %s\n", now.Format("2006/01/02 15:04:05"))
	host, _ := os.Hostname()
	addr, _ := net.InterfaceAddrs()
	fmt.Fprintf(&buf, "Running on machine: %s %v\n", host, addr)
	fmt.Fprintf(&buf, "Binary: Built with %s %s for %s/%s\n", runtime.Compiler, runtime.Version(), runtime.GOOS, runtime.GOARCH)

	n, err := sb.file.Write(buf.Bytes())
	sb.nbytes += uint64(n)
	return err
}

// createFile creates a new log file and returns the file and its filename, which contains t.
func createFile(t time.Time) (f *os.File, filename string, err error) {
	onceLogDirs.Do(createLogDir)
	name := logName(t)
	fname := filepath.Join(LogDir, name)
	f, err = os.Create(fname)
	if err != nil {
		return nil, "", fmt.Errorf("cannot create log: %v", err)
	}
	return f, fname, nil
}

func createLogDir() {
	if LogDir != "" {
		if err := os.MkdirAll(LogDir, 0755); err != nil {
			glog.Fatal(err)
		}
	} else {
		LogDir = "."
	}
}

// logName returns a new log file name containing tag, with start time t, and
// the name for the symlink for tag.
func logName(t time.Time) (name string) {
	name = fmt.Sprintf("%s.%s.%s.log.%04d%02d%02d-%02d%02d%02d.%d",
		program,
		host,
		userName,
		t.Year(),
		t.Month(),
		t.Day(),
		t.Hour(),
		t.Minute(),
		t.Second(),
		pid)
	return name
}

// shortHostname returns its argument, truncating at the first period.
// For instance, given "www.google.com" it returns "www".
func shortHostname(hostname string) string {
	if i := strings.Index(hostname, "."); i >= 0 {
		return hostname[:i]
	}
	return hostname
}

// rotatedFileNames get from disk all file names that were rotated in the past.
func rotatedFileNames() []string {
	if LogDir == "" {
		return nil
	}
	files, err := ioutil.ReadDir(LogDir)
	if err != nil {
		glog.Warningf("Can not read dir %s: %v", LogDir, err)
		return nil
	}
	pattern := fmt.Sprintf("^%s\\.%s\\.%s\\.log\\.[0-9]{8}-[0-9]{6}\\.%d$",
		program,
		host,
		userName,
		pid)
	var names []string
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		match, err := regexp.MatchString(pattern, file.Name())
		if err != nil {
			glog.Errorf("Can not do MatchString for %s, %s", pattern, file.Name())
			continue
		}
		if ! match {
			continue
		}
		names = append(names, file.Name())
	}
	return names
}

func NewFlushSyncWriter() FlushSyncWriter {
	now := time.Now()
	sb := syncBuffer{}
	sb.rotateFile(now)
	return &sb
}
