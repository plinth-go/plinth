package jobs_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/plinth-go/plinth/app"
	"github.com/plinth-go/plinth/platform/jobs"
)

func TestEmbeddableConfigFragments(t *testing.T) {
	type config struct {
		Worker    jobs.WorkerConfig    `env:""`
		Scheduler jobs.SchedulerConfig `env:""`
	}

	const prefix = "PLINTH_TEST_JOBS_CONFIG"
	template, err := app.EnvTemplate(config{}, app.WithEnvPrefix(prefix))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		prefix + "_WORKER_CONCURRENCY=4\n",
		prefix + "_WORKER_POLL_INTERVAL=500ms\n",
		prefix + "_WORKER_TIMEOUT=30s\n",
		prefix + "_WORKER_STALE_AFTER=2m\n",
		prefix + "_WORKER_RETRY_BASE=1s\n",
		prefix + "_WORKER_RETRY_MAX=1m\n",
		prefix + "_SCHEDULER_INTERVAL=1s\n",
		prefix + "_SCHEDULER_BATCH=20\n",
	} {
		if !strings.Contains(string(template), want) {
			t.Fatalf("missing %q in:\n%s", want, template)
		}
	}

	t.Setenv(prefix+"_WORKER_CONCURRENCY", "7")
	t.Setenv(prefix+"_WORKER_RETRY_BASE", "3s")
	t.Setenv(prefix+"_SCHEDULER_BATCH", "11")

	var loaded config
	if err := app.LoadEnv(context.Background(), &loaded, app.WithEnvPrefix(prefix)); err != nil {
		t.Fatal(err)
	}
	wantWorker := jobs.DefaultWorkerConfig()
	wantWorker.Concurrency = 7
	wantWorker.RetryBase = 3 * time.Second
	if loaded.Worker != wantWorker {
		t.Fatalf("Worker = %#v, want %#v", loaded.Worker, wantWorker)
	}
	wantScheduler := jobs.DefaultSchedulerConfig()
	wantScheduler.Batch = 11
	if loaded.Scheduler != wantScheduler {
		t.Fatalf("Scheduler = %#v, want %#v", loaded.Scheduler, wantScheduler)
	}

	t.Run("validation uses final env name", func(t *testing.T) {
		t.Setenv(prefix+"_WORKER_RETRY_BASE", "10s")
		t.Setenv(prefix+"_WORKER_RETRY_MAX", "1s")

		var invalid config
		err := app.LoadEnv(context.Background(), &invalid, app.WithEnvPrefix(prefix))
		if err == nil || !strings.Contains(err.Error(), prefix+"_WORKER_RETRY_MAX") || !strings.Contains(err.Error(), `validate:"gtefield=RetryBase"`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("duplicate final env key is rejected", func(t *testing.T) {
		type duplicate struct {
			Worker      jobs.WorkerConfig `env:""`
			Concurrency int               `env:"WORKER_CONCURRENCY"`
		}

		_, err := app.EnvTemplate(duplicate{}, app.WithEnvPrefix(prefix))
		if err == nil || !strings.Contains(err.Error(), "duplicates env "+prefix+"_WORKER_CONCURRENCY") {
			t.Fatalf("error = %v", err)
		}
	})
}
