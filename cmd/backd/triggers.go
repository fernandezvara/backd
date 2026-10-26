package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fernandezvara/backd/internal/appconfig"
	"github.com/fernandezvara/backd/internal/auth"
	"github.com/fernandezvara/backd/internal/mongodb"
	"github.com/fernandezvara/backd/internal/registry"
	"github.com/fernandezvara/backd/internal/triggers"
)

// defaultTriggerTimeout is the lease a job gets when its function declares no timeout.
const defaultTriggerTimeout = 30 * time.Second

// jobQueue is triggers.Queue over the realms' jobs collections.
type jobQueue struct{ a *app }

func (q jobQueue) Enqueue(ctx context.Context, realm string, jobs []triggers.Job) (int, error) {
	rl := q.a.reg.Realms[realm]
	if rl == nil || !rl.Settings.AuthEnabled {
		return 0, fmt.Errorf("realm %q has no system database to queue trigger jobs in", realm)
	}
	out := make([]auth.Job, len(jobs))
	for i, j := range jobs {
		timeout := j.Timeout
		if timeout <= 0 {
			timeout = defaultTriggerTimeout
		}
		out[i] = auth.Job{
			ID: j.ID, Database: j.Database, Function: j.Function, Input: j.Input, CallerActor: "system",
			ActsAsFunction: true, Origin: j.Origin, TimeoutMS: timeout.Milliseconds(), RequestID: j.RequestID,
			EventID: j.EventID, ChainRoot: j.ChainRoot, ChainDepth: j.ChainDepth,
			SerialKey: j.SerialKey, SerialOrder: j.SerialOrder,
		}
	}
	svc := &auth.Users{Store: mongodb.NewAuthStore(q.a.client, realm)}
	return svc.EnqueueJobs(ctx, out)
}

// Backlog counts the realm's unfinished trigger jobs.
func (q jobQueue) Backlog(ctx context.Context, realm string) (int, error) {
	return mongodb.NewAuthStore(q.a.client, realm).CountTriggerJobs(ctx)
}

// triggerLimits are a realm's limits: the instance's, lowered by the realm's own.
func (a *app) triggerLimits(reg *registry.Registry, realm string) registry.TriggerLimits {
	rl := reg.Realms[realm]
	if rl == nil {
		return a.cfg.TriggerLimits.OrDefault()
	}
	return rl.Settings.TriggerLimits(a.cfg.TriggerLimits)
}

// triggerConfig reads a realm's app configuration for the conditions that use appConfig; a
// realm that can't be read gives an empty one, so what depends on a flag does not fire.
func (a *app) triggerConfig(ctx context.Context, reg *registry.Registry, realm string) map[string]any {
	rl := reg.Realms[realm]
	if rl == nil || rl.AppConfig == nil || !rl.Settings.AuthEnabled {
		return map[string]any{}
	}
	svc := &appconfig.Service{Store: mongodb.NewAuthStore(a.client, realm), Config: rl.AppConfig}
	view, err := svc.Current(ctx)
	if err != nil {
		a.log.Error("app configuration unreadable for a trigger", "realm", realm, "error", err)
		return map[string]any{}
	}
	return appconfig.Flat(view.Values)
}

// startTriggers runs the trigger dispatcher on this instance when BACKD_TRIGGERS allows it.
// The dispatcher competes for the lease while some collection declares a trigger, and reads
// nothing otherwise. The returned function waits for it to stop and give the lease up.
func (a *app) startTriggers(ctx context.Context) func() {
	if !a.cfg.Triggers {
		a.log.Info("BACKD_TRIGGERS=false: this instance does not dispatch triggers")
		return func() {}
	}
	d := &triggers.Dispatcher{
		Source:   a.feedSource(),
		State:    &mongodb.TriggerState{Client: a.client, Deployment: a.cfg.DeploymentID},
		Queue:    jobQueue{a},
		Holder:   fmt.Sprintf("%s-%s", workerID(), time.Now().UTC().Format("150405")),
		Config:   a.triggerConfig,
		Limits:   a.triggerLimits,
		Observer: a.metrics.TriggerObserver(),
		Log:      a.log,
	}
	d.SetRegistry(a.reg)
	a.triggers = d
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := d.Run(ctx); err != nil && ctx.Err() == nil {
			a.log.Error("trigger dispatcher stopped", "error", err)
		}
	})
	return wg.Wait
}
