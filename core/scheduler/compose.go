package scheduler

// Compose Stacks on the plane side (compose-stacks.md §4): assembling one
// stack's desired state, publishing it, and recording what comes back.
//
// There is no pipeline here and that is the point. A stack has no build and no
// distribute stage, so there is no Deployment to advance — desired state moves
// when someone deploys or rolls back, and everything else is observation.

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// ConvergeStack publishes one stack's desired state so a deploy takes effect
// promptly rather than at the agent's next sync. A stack with no desired
// revision has nothing to converge toward and is a no-op — the same guard
// ConvergeApp uses.
func (s *Scheduler) ConvergeStack(ctx context.Context, stackID string) error {
	stack, err := s.store.GetComposeStack(ctx, stackID)
	if err != nil {
		return fmt.Errorf("scheduler: getting compose stack: %w", err)
	}
	if stack.DesiredRevisionID == nil {
		return nil
	}
	spec, err := s.composeSpec(ctx, stack)
	if err != nil {
		return err
	}
	data, err := proto.Marshal(&agentv1.ComposeConvergeWork{Spec: spec})
	if err != nil {
		return fmt.Errorf("scheduler: marshaling compose converge: %w", err)
	}
	// Keyed by the revision, so a redelivered converge for the same desired
	// state is deduplicated and a NEW deploy is not (rule 12).
	msgID := stackID + "." + *stack.DesiredRevisionID + ".compose"
	if err := s.bus.PublishWork(ctx, subjects.ComposeConverge(stack.ServerID), msgID, data); err != nil {
		return fmt.Errorf("scheduler: publishing compose converge: %w", err)
	}
	// The plane says "converging" only after the work is out, so a failed
	// publish never leaves a stack claiming to be doing something it is not.
	if err := s.store.SetComposeStackStatus(ctx, stackID, domain.AppDeploying, ""); err != nil {
		s.log.Error("marking compose stack converging", "stack_id", stackID, "error", err)
	}
	return nil
}

// RemoveStack publishes desired absence for a deleted stack. Called after the
// row is gone; the agent's next sync would converge eventually, but the
// explicit work item makes teardown prompt.
func (s *Scheduler) RemoveStack(ctx context.Context, serverID, stackID string, deleteVolumes bool) error {
	work := &agentv1.ComposeRemoveWork{
		IdempotencyKey: "remove-" + stackID,
		StackId:        stackID,
		DeleteVolumes:  deleteVolumes,
	}
	data, err := proto.Marshal(work)
	if err != nil {
		return fmt.Errorf("scheduler: marshaling compose removal: %w", err)
	}
	if err := s.bus.PublishWork(ctx, subjects.ComposeRemove(serverID), work.GetIdempotencyKey(), data); err != nil {
		return fmt.Errorf("scheduler: publishing compose removal: %w", err)
	}
	return nil
}

// HandleComposeStatus records one stack's observation (ADR-005: the plane
// records what the agent reports and never asserts success from a work item).
//
// Only the server the stack runs on may report it — the same reporter check
// application and database status already make (threat-model §5.2).
func (s *Scheduler) HandleComposeStatus(ctx context.Context, serverID string, st *agentv1.ComposeStatus) {
	stack, err := s.store.GetComposeStack(ctx, st.GetStackId())
	if err != nil {
		return // deleted stack; the observation is moot
	}
	if stack.ServerID != serverID {
		s.log.Warn("compose status from a server the stack does not run on",
			"stack_id", stack.ID, "reported_by", serverID, "runs_on", stack.ServerID)
		return
	}
	observedAt := s.now()
	if ts := st.GetObservedAt(); ts != nil {
		observedAt = ts.AsTime()
	}
	if err := s.store.SetComposeStackObservedStatus(ctx, st.GetStackId(),
		st.GetState(), st.GetDetail(), st.GetRevisionId(), observedAt); err != nil {
		s.log.Error("compose status: recording observation", "stack_id", st.GetStackId(), "error", err)
	}
}

// composeSpec assembles one stack's wire form. The env is unsealed here, at
// publish time, exactly as an application's is: the plaintext exists only
// inside the mTLS-carried spec (rule 23) and is never logged (rule 20).
func (s *Scheduler) composeSpec(ctx context.Context, stack domain.ComposeStack) (*agentv1.ComposeSpec, error) {
	rev, err := s.store.GetComposeRevision(ctx, *stack.DesiredRevisionID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: getting compose revision: %w", err)
	}
	env, err := s.composeEnv(ctx, stack.ID)
	if err != nil {
		return nil, err
	}
	spec := &agentv1.ComposeSpec{
		StackId:       stack.ID,
		EnvironmentId: stack.EnvironmentID,
		RevisionId:    rev.ID,
		ComposeYaml:   rev.ComposeYAML,
		Env:           env,
		Network:       "cypher-" + stack.EnvironmentID,
	}
	if stack.Route.Domain != "" {
		spec.Route = &agentv1.ComposeRoute{
			// Verified like every other domain: a route the operator does not
			// own is blanked rather than refused, so the deploy still lands
			// (dns-automation.md §4.2).
			Domain:  s.routableDomain(ctx, stack.Route.Domain),
			Https:   stack.Route.HTTPS,
			Service: stack.Route.Service,
			Port:    uint32(stack.Route.Port), //nolint:gosec // validated 1–65535
		}
	}
	return spec, nil
}

// composeEnv unseals a stack's variables. A value that will not open is a
// sealing-key problem, not this stack's data, so it fails loudly rather than
// shipping a file with a hole in it.
func (s *Scheduler) composeEnv(ctx context.Context, stackID string) (map[string]string, error) {
	sealed, err := s.store.ListComposeEnvVars(ctx, stackID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: listing compose env vars: %w", err)
	}
	if len(sealed) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(sealed))
	for _, v := range sealed {
		plain, oerr := s.opener.Open(v.ValueCT, v.ValueNonce)
		if oerr != nil {
			return nil, fmt.Errorf("scheduler: unsealing compose env var %s: %w", v.Key, oerr)
		}
		env[v.Key] = string(plain)
	}
	return env, nil
}

// composeSpecsFor is the desired-state half: every stack on one server that has
// something to run. A stack with no desired revision is omitted, which reads as
// "nothing to converge", not "remove it" — it has never been deployed.
func (s *Scheduler) composeSpecsFor(ctx context.Context, serverID string) ([]*agentv1.ComposeSpec, error) {
	stacks, err := s.store.ListComposeStacksByServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: listing compose stacks for %s: %w", serverID, err)
	}
	out := make([]*agentv1.ComposeSpec, 0, len(stacks))
	for _, stack := range stacks {
		if stack.DesiredRevisionID == nil {
			continue
		}
		spec, err := s.composeSpec(ctx, stack)
		if err != nil {
			// A missing revision row is this stack's problem; omitting the
			// stack from a sync reply would bring it DOWN, so it is logged and
			// the rest of the node's set still converges.
			if errors.Is(err, store.ErrNotFound) {
				s.log.Error("desired state: omitting a stack whose desired revision row is gone",
					"server_id", serverID, "stack_id", stack.ID)
				continue
			}
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}
