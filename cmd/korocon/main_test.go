package main

import (
	"bufio"
	"bytes"
	"context"
	issueworkflow "github.com/coco-papiyon/korocon/internal/issue"
	"io"
	"strings"
	"testing"
)

type blockingReader struct {
	started chan struct{}
	release chan struct{}
}

func TestShowPullRequestsReportsEmptyJSONList(t *testing.T) {
	old := runGitHubCommand
	runGitHubCommand = func(context.Context, string, ...string) ([]byte, error) { return []byte("[]"), nil }
	defer func() { runGitHubCommand = old }()
	var out bytes.Buffer
	hasPR, err := showPullRequests(context.Background(), &out, ".")
	if err != nil || hasPR || out.Len() != 0 {
		t.Fatalf("hasPR=%v err=%v output=%q", hasPR, err, out.String())
	}
}

func TestSelectGitHubInformationAcceptsEmptyAsIssue(t *testing.T) {
	old := loadIssueWorkflow
	loadIssueWorkflow = func(_ context.Context, _ string, number int, workspace string) (*issueworkflow.Workflow, error) {
		return &issueworkflow.Workflow{Issue: issueworkflow.Issue{Number: number, State: "OPEN"}, Phase: issueworkflow.PhaseDesign}, nil
	}
	defer func() { loadIssueWorkflow = old }()
	var out bytes.Buffer
	_, selected, err := selectGitHubInformation(context.Background(), bytes.NewBufferString("\n42\n"), &out, ".", ".workspace")
	if err != nil || selected == nil || selected.Issue.Number != 42 {
		t.Fatalf("selected=%+v err=%v output=%q", selected, err, out.String())
	}
	if !strings.Contains(out.String(), "取得する情報を選択してください (ISSUE/PR):") {
		t.Fatalf("prompt missing: %q", out.String())
	}
}

func TestSelectGitHubInformationRetriesAfterClosedIssue(t *testing.T) {
	old := loadIssueWorkflow
	loadIssueWorkflow = func(_ context.Context, _ string, number int, workspace string) (*issueworkflow.Workflow, error) {
		state := "CLOSED"
		if number == 2 {
			state = "OPEN"
		}
		return &issueworkflow.Workflow{Issue: issueworkflow.Issue{Number: number, State: state}, Phase: issueworkflow.PhaseDesign}, nil
	}
	defer func() { loadIssueWorkflow = old }()
	var out bytes.Buffer
	_, selected, err := selectGitHubInformation(context.Background(), bytes.NewBufferString("issue\n1\nissue\n2\n"), &out, ".", ".workspace")
	if err != nil || selected == nil || selected.Issue.Number != 2 {
		t.Fatalf("selected=%+v err=%v output=%q", selected, err, out.String())
	}
	if !strings.Contains(out.String(), "Issue #1 は OPEN ではありません（CLOSED）。") {
		t.Fatalf("closed issue message missing: %q", out.String())
	}
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
