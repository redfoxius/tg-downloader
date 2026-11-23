package web

import (
	"context"
	"testing"
	"time"
)

func waitIdle(t *testing.T, j *jobs) jobView {
	t.Helper()
	for i := 0; i < 200; i++ {
		if v := j.view(); !v.Running {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return jobView{}
}

func TestOneJobAtATimeAndCancel(t *testing.T) {
	j := &jobs{base: context.Background()}
	started := make(chan struct{})
	err := j.start("a", func(ctx context.Context, logf func(string, ...any)) error {
		logf("hello %d", 1)
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := j.start("b", nil); err != errBusy {
		t.Fatalf("second start: %v", err)
	}
	j.stop()
	v := waitIdle(t, j)
	if v.Error != "cancelled" || len(v.Log) != 1 || v.Log[0] != "hello 1" {
		t.Errorf("view: %+v", v)
	}
	if err := j.start("c", func(context.Context, func(string, ...any)) error { return nil }); err != nil {
		t.Errorf("start after finish: %v", err)
	}
}
