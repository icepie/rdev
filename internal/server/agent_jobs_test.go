package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAgentJobStreamsAndCompletesAfterWaitCancellation(t *testing.T) {
	delivered := make(chan agentJobSnapshot, 1)
	jobs := newAgentJobs(func(job agentJobSnapshot) { delivered <- job })
	defer jobs.Close()
	streamed, finish := make(chan struct{}), make(chan struct{})
	job, err := jobs.Start("shell", "build", func(ctx context.Context, write func(string)) (string, error) {
		write("compiling\n")
		close(streamed)
		select {
		case <-finish:
			return "build passed", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	<-streamed
	snapshot, err := jobs.Read(job.ID)
	if err != nil || snapshot.Output != "compiling\n" || snapshot.State != "running" {
		t.Fatalf("live output: %+v, %v", snapshot, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := jobs.Wait(ctx, job.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error: %v", err)
	}
	if !jobs.Running() {
		t.Fatal("canceling the wait canceled the job")
	}
	close(finish)
	select {
	case result := <-delivered:
		if result.State != "completed" || result.Result != "build passed" || result.Finished == nil {
			t.Fatalf("completion: %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion was not delivered")
	}
}

func TestAgentJobsCloseCancelsAndPreventsNewWork(t *testing.T) {
	jobs := newAgentJobs(func(agentJobSnapshot) { t.Error("closed thread delivered a stale result") })
	job, err := jobs.Start("shell", "watch", func(ctx context.Context, _ func(string)) (string, error) { <-ctx.Done(); return "", ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	jobs.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := jobs.Wait(ctx, job.ID)
	if err != nil || result.State != "canceled" {
		t.Fatalf("close result: %+v %v", result, err)
	}
	if _, err := jobs.Start("shell", "late", nil); err == nil {
		t.Fatal("closed thread admitted new work")
	}
}

func TestAgentJobsLimitAndThreadIsolation(t *testing.T) {
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	other := newAgentJobs(nil)
	defer other.Close()
	var id string
	for range agentMaxRunningJobs {
		job, err := jobs.Start("shell", "wait", func(ctx context.Context, _ func(string)) (string, error) { <-ctx.Done(); return "", ctx.Err() })
		if err != nil {
			t.Fatal(err)
		}
		id = job.ID
	}
	if _, err := jobs.Start("shell", "overflow", nil); err == nil {
		t.Fatal("running limit not enforced")
	}
	if err := other.Cancel(id); err == nil {
		t.Fatal("another thread could cancel a job")
	}
	if _, err := other.Read(id); err == nil {
		t.Fatal("another thread could read a job")
	}
}

func TestAgentJobOutputIsBoundedAndPanicIsTerminal(t *testing.T) {
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	job, err := jobs.Start("subagent", "inspect", func(_ context.Context, write func(string)) (string, error) {
		write(strings.Repeat("界", agentJobOutputLimit))
		panic("worker failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := jobs.Wait(ctx, job.ID)
	if err != nil || result.State != "failed" || !strings.Contains(result.Error, "worker failed") {
		t.Fatalf("panic result: %+v %v", result, err)
	}
	if len(result.Output) > agentJobOutputLimit || !utf8.ValidString(result.Output) {
		t.Fatalf("invalid bounded output: %d bytes", len(result.Output))
	}
	if jobs.Running() {
		t.Fatal("panic left job running")
	}
}

func TestAgentJobErrorIsBoundedInReadListAndCompletion(t *testing.T) {
	delivered := make(chan agentJobSnapshot, 1)
	jobs := newAgentJobs(func(job agentJobSnapshot) { delivered <- job })
	defer jobs.Close()
	job, err := jobs.Start("subagent", "failing provider", func(context.Context, func(string)) (string, error) {
		return "", errors.New(strings.Repeat("错误详情", agentJobOutputLimit) + " final cause")
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := jobs.Wait(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "failed" || len(result.Error) > agentJobOutputLimit || !utf8.ValidString(result.Error) || !strings.HasSuffix(result.Error, " final cause") {
		t.Fatalf("invalid bounded failure: state=%s bytes=%d", result.State, len(result.Error))
	}
	listed := jobs.List()
	if len(listed) != 1 || listed[0].Error != result.Error {
		t.Fatal("list did not retain bounded error")
	}
	select {
	case notification := <-delivered:
		if notification.Error != result.Error {
			t.Fatal("completion delivered unbounded error")
		}
	case <-ctx.Done():
		t.Fatal("completion not delivered")
	}
}

func TestCancelAllReleasesSlotsAfterWorkersStop(t *testing.T) {
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	ids := make([]string, 0, agentMaxRunningJobs)
	for range agentMaxRunningJobs {
		job, err := jobs.Start("shell", "service", func(ctx context.Context, _ func(string)) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, job.ID)
	}
	jobs.CancelAll()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, id := range ids {
		job, err := jobs.Wait(ctx, id)
		if err != nil || job.State != "canceled" {
			t.Fatalf("cancel %s: %+v %v", id, job, err)
		}
	}
	if jobs.Running() {
		t.Fatal("canceled workers retained running slots")
	}
	replacement, err := jobs.Start("shell", "replacement", func(context.Context, func(string)) (string, error) { return "done", nil })
	if err != nil {
		t.Fatal(err)
	}
	result, err := jobs.Wait(ctx, replacement.ID)
	if err != nil || result.State != "completed" {
		t.Fatalf("replacement: %+v %v", result, err)
	}
}

// Pause a waiter after it captured the job but before selecting on done.
// This makes retention eviction before the waiter wakes deterministic.
type delayedJobWaitContext struct {
	context.Context
	captured chan struct{}
	resume   chan struct{}
}

func (c delayedJobWaitContext) Done() <-chan struct{} {
	close(c.captured)
	<-c.resume
	return nil
}

func TestJobWaitSurvivesRetentionEviction(t *testing.T) {
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	finish := make(chan struct{})
	job, err := jobs.Start("shell", "oldest", func(ctx context.Context, _ func(string)) (string, error) {
		select {
		case <-finish:
			return "retained result", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := delayedJobWaitContext{Context: context.Background(), captured: make(chan struct{}), resume: make(chan struct{})}
	defer close(ctx.resume)
	result := make(chan agentJobSnapshot, 1)
	failure := make(chan error, 1)
	go func() { snapshot, err := jobs.Wait(ctx, job.ID); result <- snapshot; failure <- err }()
	<-ctx.captured
	close(finish)
	timeout, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := jobs.Wait(timeout, job.ID); err != nil {
		t.Fatal(err)
	}
	for range agentMaxRetainedJobs {
		next, err := jobs.Start("shell", "new", func(context.Context, func(string)) (string, error) { return "", nil })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := jobs.Wait(timeout, next.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := jobs.Read(job.ID); err == nil {
		t.Fatal("old job was not evicted")
	}
	// Resume without closing so the deferred close remains safe.
	ctx.resume <- struct{}{}
	select {
	case snapshot := <-result:
		if err := <-failure; err != nil || snapshot.Result != "retained result" {
			t.Fatalf("lost completed result: %+v %v", snapshot, err)
		}
	case <-timeout.Done():
		t.Fatal("waiter did not finish")
	}
}

func TestCancelAllIsThreadLocalAndAllowsLaterJobs(t *testing.T) {
	jobs, other := newAgentJobs(nil), newAgentJobs(nil)
	defer jobs.Close()
	defer other.Close()
	work := func(ctx context.Context, _ func(string)) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	first, _ := jobs.Start("shell", "first", work)
	second, _ := jobs.Start("shell", "second", work)
	outside, _ := other.Start("shell", "outside", work)
	jobs.CancelAll()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, id := range []string{first.ID, second.ID} {
		result, err := jobs.Wait(ctx, id)
		if err != nil || result.State != "canceled" {
			t.Fatalf("cancel all: %+v %v", result, err)
		}
	}
	result, _ := other.Read(outside.ID)
	if result.State != "running" {
		t.Fatal("canceled another thread's job")
	}
	if _, err := jobs.Start("shell", "later", work); err != nil {
		t.Fatal("cancel all closed the job manager", err)
	}
}

func TestJobCountChangesOutsideLock(t *testing.T) {
	changed := make(chan int, 2)
	manager := newAgentJobs(nil)
	manager.onChange = func() { changed <- manager.RunningCount() }
	release := make(chan struct{})
	_, err := manager.Start("shell", "test", func(context.Context, func(string)) (string, error) {
		<-release
		return "done", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count := <-changed; count != 1 {
		t.Fatalf("started count=%d", count)
	}
	close(release)
	select {
	case count := <-changed:
		if count != 0 {
			t.Fatalf("finished count=%d", count)
		}
	case <-time.After(time.Second):
		t.Fatal("completion did not publish count")
	}
}

func TestCancellationPendingRemainsVisibleUntilWorkersExit(t *testing.T) {
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	ids := make([]string, 0, agentMaxRunningJobs)
	for range agentMaxRunningJobs {
		job, err := jobs.Start("shell", "slow cleanup", func(ctx context.Context, _ func(string)) (string, error) {
			<-ctx.Done()
			<-release
			return "", ctx.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, job.ID)
	}
	if err := jobs.Cancel(ids[0]); err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range jobs.List() {
		if snapshot.CancelRequested != (snapshot.ID == ids[0]) {
			t.Fatal("single-job cancellation leaked across jobs")
		}
	}
	jobs.CancelAll()
	jobs.CancelAll() // repeated requests are idempotent while cleanup is blocked
	for _, id := range ids {
		snapshot, err := jobs.Read(id)
		if err != nil || snapshot.State != "running" || !snapshot.CancelRequested || snapshot.Finished != nil {
			t.Fatalf("pending cancellation lost: %+v %v", snapshot, err)
		}
	}
	if jobs.RunningCount() != agentMaxRunningJobs {
		t.Fatal("cancel request released running slots")
	}
	if _, err := jobs.Start("shell", "over quota", func(context.Context, func(string)) (string, error) { return "", nil }); err == nil {
		t.Fatal("admitted replacement before cleanup completed")
	}
	finish()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for _, id := range ids {
		snapshot, err := jobs.Wait(ctx, id)
		if err != nil || snapshot.State != "canceled" || snapshot.CancelRequested {
			t.Fatalf("terminal cancellation: %+v %v", snapshot, err)
		}
		if err := jobs.Cancel(id); err != nil {
			t.Fatal(err)
		}
		snapshot, _ = jobs.Read(id)
		if snapshot.CancelRequested {
			t.Fatal("completed task returned to stopping state")
		}
	}
}

func TestCancellationNotifiesOutsideLockOnlyOnce(t *testing.T) {
	jobs := newAgentJobs(nil)
	defer jobs.Close()
	release := make(chan struct{})
	defer close(release)
	changes := make(chan []agentJobSnapshot, 4)
	jobs.onChange = func() { changes <- jobs.List() }
	job, err := jobs.Start("shell", "cleanup", func(ctx context.Context, _ func(string)) (string, error) {
		<-ctx.Done()
		<-release
		return "", ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-changes // admission
	if err := jobs.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	if snapshot := <-changes; len(snapshot) != 1 || !snapshot[0].CancelRequested {
		t.Fatal("cancellation change was not readable")
	}
	if err := jobs.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	jobs.CancelAll()
	select {
	case <-changes:
		t.Fatal("repeated cancellation published duplicate changes")
	default:
	}
}
