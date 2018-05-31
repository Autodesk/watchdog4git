package attributes

// Most of the code here is borrowed from:
// https://github.com/git-lfs/git-lfs/blob/master/git/attribs.go
//
// Maybe we can/should include the Git LFS "git" package as-is at
// some point?

import (
	"bufio"
	"bytes"
	"strings"

	"github.com/git-lfs/git-lfs/filepathfilter"
)

// GetAttributePaths returns a filter that combine the entries in
// .gitattributes which are configured with the 'filter=lfs' attribute
// attributesText is the contents of the .gitattributes file for the repo
func GetAttributePaths(attributesText string) *filepathfilter.Filter {
	var paths []string

	le := &lineEndingSplitter{}
	scanner := bufio.NewScanner(strings.NewReader(attributesText))
	scanner.Split(le.ScanLines)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Ignore commented lines
		if strings.HasPrefix(line, "#") {
			continue
		}

		// Check for filter=lfs (signifying that LFS is tracking
		// this file).
		if strings.Contains(line, "filter=lfs") {
			fields := strings.Fields(line)
			paths = append(paths, fields[0])
		}
	}

	if len(paths) == 0 {
		return nil
	}
	return filepathfilter.New(paths, nil)
}

// copies bufio.ScanLines(), counting LF vs CRLF in a file
type lineEndingSplitter struct {
	LFCount   int
	CRLFCount int
}

func (s *lineEndingSplitter) ScanLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		// We have a full newline-terminated line.
		return i + 1, s.dropCR(data[0:i]), nil
	}
	// If we're at EOF, we have a final, non-terminated line. Return it.
	if atEOF {
		return len(data), data, nil
	}
	// Request more data.
	return 0, nil, nil
}

// dropCR drops a terminal \r from the data.
func (s *lineEndingSplitter) dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		s.CRLFCount++
		return data[0 : len(data)-1]
	}
	s.LFCount++
	return data
}
