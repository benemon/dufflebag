package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
)

// zipNames reads a module zip and returns its file names.
//
// archive/zip needs a ReaderAt, so the body is buffered. The SDK zip is a few
// megabytes and this runs on a schedule, which is well inside what a scheduled
// job can hold — streaming it would mean reimplementing zip's central directory
// walk for no benefit.
func zipNames(body io.Reader) ([]string, error) {
	buffer, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read module zip: %w", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(buffer), int64(len(buffer)))
	if err != nil {
		return nil, fmt.Errorf("open module zip: %w", err)
	}
	names := make([]string, 0, len(archive.File))
	for _, file := range archive.File {
		names = append(names, file.Name)
	}
	return names, nil
}
