package workermanager

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/forkbombeu/credimi-runner/pkg/utils"
	"github.com/forkbombeu/credimi/pkg/workflowengine"
	"github.com/forkbombeu/credimi/pkg/workflowengine/activities"
	"github.com/forkbombeu/credimi/pkg/workflowengine/workflows"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

var clientCache sync.Map

// RunTemporalWorker returns a function suitable for Process.RunFunc
func RunTemporalWorker(namespace string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		c, err := getTemporalClientWithNamespace(namespace)
		if err != nil {
			return err
		}
		runnerID := utils.GetEnvironmentVariable("CREDIMI_RUNNER_ID", "", true)
		taskqueue := fmt.Sprintf("%s-%s", runnerID, "TaskQueue")
		w := worker.New(c, taskqueue, worker.Options{})

		// Register workflows
		for _, wf := range []workflowengine.Workflow{workflows.NewMobileAutomationWorkflow()} {
			w.RegisterWorkflowWithOptions(wf.Workflow, workflow.RegisterOptions{Name: wf.Name()})
		}

		// Register activities
		for _, act := range []workflowengine.ExecutableActivity{
			activities.NewHTTPActivity(),
			activities.NewRunMobileFlowActivity(),
			activities.NewStartEmulatorActivity(),
			activities.NewApkInstallActivity(),
			activities.NewStartRecordingActivity(),
			activities.NewStopRecordingActivity(),
			// activities.NewUnlockEmulatorActivity(),
			activities.NewCleanupDeviceActivity(),
		} {
			w.RegisterActivityWithOptions(act.Execute, activity.RegisterOptions{Name: act.Name()})
		}

		// Shutdown channel
		shutdownCh := make(chan interface{})
		go func() {
			<-ctx.Done()
			close(shutdownCh)
		}()

		log.Printf("Temporal worker running for namespace %s", namespace)
		if err := w.Run(shutdownCh); err != nil {
			log.Printf("Temporal worker stopped with error: %v", err)
			return err
		}

		log.Printf("Temporal worker stopped for namespace %s", namespace)
		return nil
	}
}

func getTemporalClientWithNamespace(namespace string) (client.Client, error) {
	if c, ok := clientCache.Load(namespace); ok {
		return c.(client.Client), nil
	}
	hostPort := utils.GetEnvironmentVariable("TEMPORAL_ADDRESS", client.DefaultHostPort)
	c, err := client.NewLazyClient(client.Options{
		HostPort:  hostPort,
		Namespace: namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to create client: %w", err)
	}

	clientCache.Store(namespace, c)
	return c, nil
}
