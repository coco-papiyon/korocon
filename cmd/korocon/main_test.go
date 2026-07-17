package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"testing"
)

type blockingReader struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingReader) Read([]byte) (int, error) {
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	<-r.release
	return 0, io.EOF
}

func TestReadStringContextStopsWhenContextIsCanceled(t *testing.T) {
	blocking := &blockingReader{started: make(chan struct{}), release: make(chan struct{})}
	reader := bufio.NewReader(blocking)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := readStringContext(ctx, reader)
		result <- err
	}()
	<-blocking.started
	cancel()
	if err := <-result; err != context.Canceled {
		t.Fatalf("error = %v, want context canceled", err)
	}
	close(blocking.release)
}

func TestRemainingInputPreservesBufferedData(t *testing.T) {
	original := bytes.NewBufferString("issue\n42\nnext prompt\n")
	reader := bufio.NewReader(original)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	remaining := remainingInput(original, reader)
	data, err := io.ReadAll(remaining)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "next prompt\n" {
		t.Fatalf("remaining input = %q", data)
	}
}

func TestRemainingInputReturnsOriginalWithoutReadAhead(t *testing.T) {
	original := bytes.NewBuffer(nil)
	reader := bufio.NewReader(original)
	if got := remainingInput(original, reader); got != original {
		t.Fatal("original terminal reader was not restored")
	}
}
