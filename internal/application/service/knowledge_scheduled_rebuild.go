package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// KnowledgeScheduledRebuildScheduler (定时重建调度器) walks every KB in
// the system on a fixed cadence and, for each KB whose
// ScheduledRebuildConfig.Enabled is true, runs a per-KB repair tick at
// the cadence declared on the KB itself.
//
// Design:
//
//   - One in-process goroutine. No global timer per KB — a single
//     hardcoded poll loop ("scheduledRebuildPollInterval", default 5s)
//     wakes the scheduler, which queries the opt-in set and then
//     per-KB decides whether to tick.
//
//   - Per-KB interval tracking is held in memory. Restarting the
//     process loses the counter; the next poll then ticks every opted-
//     in KB once (acceptable — they're just freshly "stale" from the
//     scheduler's POV). KB-side knobs (interval, batch, statuses,
//     min-age) are read live from the row every poll so a UI edit takes
//     effect immediately, no restart required.
//
//   - Multi-replica: each replica runs its own scheduler. ReparseKnowledge
//     is idempotent (flips failed→pending before enqueue, and the
//     status-machine guards reject re-enqueues of "processing" rows),
//     so two replicas ticking the same KB in the same wall-clock
//     window can't double-repair the same document.
//
// This replaces the legacy parser.auto_repair_* global settings and
// the global KnowledgeRepairScheduler that read them. The previous
// "one global scheduler, all KBs share interval/batch/min-age/statuses"
// design was too coarse — operators typically want per-KB tuning
// (interval on a small KB ≠ interval on a 100k-doc KB).
type KnowledgeScheduledRebuildScheduler struct {
	kgService  interfaces.KnowledgeService
	kbRepo     interfaces.KnowledgeBaseRepository
	tenantRepo interfaces.TenantRepository

	// stopped is set by Stop() to break the run loop after the current
	// poll finishes. Read on every iteration and between KB ticks so a
	// graceful shutdown can complete an in-flight KB.
	stopped atomicBool

	// lastTickAt remembers when each KB was last serviced. A KB is
	// eligible for a tick when (now - lastTickAt[kbID]) >= interval.
	// Missing keys are treated as zero time → eligible on the next
	// poll (first time we see this KB). Guarded by mu; reads/writes
	// are O(1) so contention is negligible.
	mu         sync.Mutex
	lastTickAt map[string]time.Time
}

// atomicBool is a tiny one-byte atomic flag. Using sync/atomic.Bool
// would also work but importing it just for one bool feels heavy; the
// stdlib helper is intentional.
type atomicBool struct {
	v   bool
	mtx sync.Mutex
}

func (a *atomicBool) Store(b bool) {
	a.mtx.Lock()
	a.v = b
	a.mtx.Unlock()
}

func (a *atomicBool) Load() bool {
	a.mtx.Lock()
	defer a.mtx.Unlock()
	return a.v
}

// scheduledRebuildPollInterval is the fixed cadence at which the
// scheduler wakes up to re-evaluate which KBs need a tick. Smaller
// values give faster reaction time at the cost of more DB reads;
// larger values amortize the LIST cost but delay the first tick of a
// freshly opted-in KB by up to one interval.
//
// 5s is a reasonable middle ground: cheap for typical deployments
// (KB list fits in a single indexed query), fast enough that a
// 60s-interval KB still sees its first tick within ~5s of being
// opted in. Operators with thousands of opted-in KBs and a tight DB
// can lower this; nobody should need to raise it.
const scheduledRebuildPollInterval = 5 * time.Second

// repairIntervalFloor / repairIntervalCeiling bound the per-KB
// declared interval at runtime. Values below 1s would cause hot-spin;
// values above 24h are almost certainly an operator typo.
const (
	repairIntervalFloor   = 1 * time.Second
	repairIntervalCeiling = 24 * time.Hour
)

// repairBatchSizeCap mirrors the historical safety cap on the
// document-promotion scheduler — defensive against an operator typo
// that would otherwise flood the asynq queue.
const repairBatchSizeCap = 32

// NewKnowledgeScheduledRebuildScheduler builds a scheduler. Returns
// nil when any required dependency is missing so callers (lite / test
// wiring) can skip the feature without nil-checking the goroutine.
func NewKnowledgeScheduledRebuildScheduler(
	kgService interfaces.KnowledgeService,
	kbRepo interfaces.KnowledgeBaseRepository,
	tenantRepo interfaces.TenantRepository,
) *KnowledgeScheduledRebuildScheduler {
	if kgService == nil || kbRepo == nil || tenantRepo == nil {
		logger.Warnf(context.Background(),
			"[scheduled_rebuild] scheduler step dependency_missing: kgService=%v kbRepo=%v tenantRepo=%v scheduler_disabled=true",
			kgService != nil, kbRepo != nil, tenantRepo != nil,
		)
		return nil
	}
	return &KnowledgeScheduledRebuildScheduler{
		kgService:  kgService,
		kbRepo:     kbRepo,
		tenantRepo: tenantRepo,
		lastTickAt: make(map[string]time.Time),
	}
}

// Start launches the scheduler goroutine. Returns immediately. The
// goroutine exits when ctx is cancelled OR Stop() is called.
func (s *KnowledgeScheduledRebuildScheduler) Start(ctx context.Context) {
	if s == nil {
		return
	}
	logger.Info(ctx, "[scheduled_rebuild] scheduler step start")
	go s.runLoop(ctx)
}

// Stop signals the run loop to exit after the current poll finishes.
// Safe to call multiple times; idempotent.
func (s *KnowledgeScheduledRebuildScheduler) Stop() {
	if s == nil {
		return
	}
	s.stopped.Store(true)
}

// runLoop is the scheduler's only goroutine. Each iteration is a
// fixed-cadence poll (scheduledRebuildPollInterval) so per-KB knobs
// can be re-read live without a restart.
func (s *KnowledgeScheduledRebuildScheduler) runLoop(ctx context.Context) {
	// Run an initial tick immediately so a fresh deployment doesn't
	// have to wait up to scheduledRebuildPollInterval before reacting
	// to rows left over from a previous run that crashed mid-rebuild.
	s.poll(ctx)

	for {
		if s.stopped.Load() {
			logger.Info(ctx, "[scheduled_rebuild] scheduler step stop_signal")
			return
		}
		timer := time.NewTimer(scheduledRebuildPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			logger.Info(ctx, "[scheduled_rebuild] scheduler step ctx_cancelled")
			return
		case <-timer.C:
			s.poll(ctx)
		}
	}
}

// poll is one iteration of the scheduler. Reads the current opt-in KB
// set, then per-KB checks whether the declared interval has elapsed
// since the last tick on that KB. Errors are logged, never propagated
// — the scheduler must not panic the host process on a transient DB
// hiccup.
func (s *KnowledgeScheduledRebuildScheduler) poll(ctx context.Context) {
	if s.stopped.Load() {
		return
	}

	kbs, err := s.kbRepo.ListScheduledRebuildKBs(ctx)
	if err != nil {
		logger.Warnf(ctx,
			"[scheduled_rebuild] poll step list_kbs failed: err=%v",
			err,
		)
		return
	}
	// Filter out disabled / nil config rows. The repository already
	// filters on Postgres, but sqlite and any future backends that
	// fall through the "load everything" path need a final in-Go check
	// — and the cost is one bool dereference per row.
	enabledKBs := make([]*types.KnowledgeBase, 0, len(kbs))
	disabledKBs := 0
	for _, kb := range kbs {
		if s.stopped.Load() {
			return
		}
		if kb == nil {
			disabledKBs++
			continue
		}
		if !kb.ScheduledRebuildConfig.IsEnabled() {
			disabledKBs++
			logger.Debugf(ctx,
				"[scheduled_rebuild] poll skip go_layer_filter: kb=%s config_nil=%v enabled=%v",
				kb.ID,
				kb.ScheduledRebuildConfig == nil,
				kb.ScheduledRebuildConfig != nil && kb.ScheduledRebuildConfig.Enabled,
			)
			continue
		}
		enabledKBs = append(enabledKBs, kb)
	}

	// Poll-start summary. Without this line, an empty opt-in set is
	// indistinguishable from "the scheduler never runs" — operators
	// have asked for visibility on this exact scenario.
	logger.Infof(ctx,
		"[scheduled_rebuild] poll step start: raw=%d enabled=%d skipped_disabled=%d",
		len(kbs), len(enabledKBs), disabledKBs,
	)

	now := time.Now()
	var ticked, skippedNotDue, repairedTotal int
	for _, kb := range enabledKBs {
		if s.stopped.Load() {
			return
		}
		s.mu.Lock()
		last := s.lastTickAt[kb.ID]
		s.mu.Unlock()
		interval := s.resolveInterval(kb.ScheduledRebuildConfig.IntervalSeconds)
		if !s.shouldTickKB(kb.ID, now, kb.ScheduledRebuildConfig.IntervalSeconds) {
			skippedNotDue++
			logger.Debugf(ctx,
				"[scheduled_rebuild] poll skip interval_not_due: kb=%s last_tick=%s interval=%s elapsed=%s",
				kb.ID,
				formatTickStamp(last),
				interval,
				now.Sub(last).String(),
			)
			continue
		}
		ticked++
		if repaired, tErr := s.tickKB(ctx, kb, now); tErr != nil {
			logger.Warnf(ctx,
				"[scheduled_rebuild] poll step tick_failed: kb=%s err=%v",
				kb.ID, tErr,
			)
			continue
		} else if repaired > 0 {
			repairedTotal += repaired
			logger.Infof(ctx,
				"[scheduled_rebuild] poll step repaired: kb=%s repaired=%d interval=%ds batch=%d",
				kb.ID, repaired,
				kb.ScheduledRebuildConfig.IntervalSeconds,
				kb.ScheduledRebuildConfig.BatchSize,
			)
		}
		s.markTickAt(kb.ID, now)
	}

	// Poll-end summary. Always log — even when 0 — so an idle
	// scheduler is observable (helps confirm the goroutine is alive
	// when an operator reports "nothing is happening").
	logger.Infof(ctx,
		"[scheduled_rebuild] poll step done: ticked=%d skipped_not_due=%d repaired=%d",
		ticked, skippedNotDue, repairedTotal,
	)
}

// formatTickStamp returns a stable RFC3339 string for "never ticked"
// (zero time) so debug logs are unambiguous. Pulled out as a helper
// because the zero-time string varies between Go versions.
func formatTickStamp(t time.Time) string {
	if t.IsZero() {
		return "<never>"
	}
	return t.Format(time.RFC3339)
}

// shouldTickKB returns true when the KB's declared interval has
// elapsed since its last tick (or it has never been ticked). Centralized
// here so the per-KB clamp logic stays in one place.
func (s *KnowledgeScheduledRebuildScheduler) shouldTickKB(
	kbID string, now time.Time, intervalSeconds int,
) bool {
	interval := s.resolveInterval(intervalSeconds)
	s.mu.Lock()
	last := s.lastTickAt[kbID]
	s.mu.Unlock()
	return now.Sub(last) >= interval
}

// markTickAt records the last tick time for a KB. Called after a
// successful (or attempted) tick regardless of outcome so a flapping
// KB can't hot-spin on the next poll.
func (s *KnowledgeScheduledRebuildScheduler) markTickAt(kbID string, at time.Time) {
	s.mu.Lock()
	s.lastTickAt[kbID] = at
	s.mu.Unlock()
}

// resolveInterval clamps the per-KB declared interval to
// [repairIntervalFloor, repairIntervalCeiling]. Out-of-range values are
// silently corrected (with a warning log) so a typo doesn't brick the
// scheduler. Defaults to 60s when the operator leaves it at zero —
// matches the historical default and the explicit operator request.
func (s *KnowledgeScheduledRebuildScheduler) resolveInterval(raw int) time.Duration {
	if raw <= 0 {
		raw = 60
	}
	d := time.Duration(raw) * time.Second
	if d < repairIntervalFloor {
		return repairIntervalFloor
	}
	if d > repairIntervalCeiling {
		return repairIntervalCeiling
	}
	return d
}

// resolveBatchSize clamps the per-KB declared batch_size to
// [1, repairBatchSizeCap]. Default 1 matches the operator's
// "每次查询一个出来" requirement.
func (s *KnowledgeScheduledRebuildScheduler) resolveBatchSize(raw int) int {
	if raw <= 0 {
		raw = 1
	}
	if raw > repairBatchSizeCap {
		raw = repairBatchSizeCap
	}
	return raw
}

// isInFlightNonTerminal checks whether the given knowledge row is in a
// non-terminal parse_status. Used by sync mode to decide whether the
// scheduler may trigger the *next* candidate. A terminal status
// (completed / failed / cancelled / deleting) is considered "the
// previous run is over", so the scheduler proceeds even if the
// snapshotted document is still around (failed documents unblock the
// next trigger instead of deadlocking the scheduler). A missing row is
// treated as terminal so a deleted document does not pin the KB.
func (*KnowledgeScheduledRebuildScheduler) isInFlightNonTerminal(parseStatus string) bool {
	switch parseStatus {
	case types.ParseStatusPending,
		types.ParseStatusUploaded,
		types.ParseStatusProcessing,
		types.ParseStatusFinalizing:
		return true
	}
	return false
}

// recordLastTriggered writes the "this document was just kicked off
// for rebuild" snapshot onto the KB row. Used only by sync mode —
// async mode doesn't need to remember which candidate it picked last,
// by definition. The repository Save() handles JSONB marshalling via
// ScheduledRebuildConfig.Value().
func (s *KnowledgeScheduledRebuildScheduler) recordLastTriggered(
	ctx context.Context,
	kb *types.KnowledgeBase,
	knowledge *types.Knowledge,
	now time.Time,
) {
	if kb == nil || kb.ScheduledRebuildConfig == nil || knowledge == nil {
		return
	}
	kb.ScheduledRebuildConfig.LastTriggeredKnowledgeID = knowledge.ID
	kb.ScheduledRebuildConfig.LastTriggeredKnowledgeName = knowledge.FileName
	kb.ScheduledRebuildConfig.LastTriggeredAt = &now
	kb.UpdatedAt = now
	if err := s.kbRepo.UpdateKnowledgeBase(ctx, kb); err != nil {
		logger.Warnf(ctx,
			"[scheduled_rebuild] record_last_triggered persist_failed: kb=%s knowledge=%s err=%v",
			kb.ID, knowledge.ID, err,
		)
	}
}

// clearLastTriggered drops the "previous run is over" snapshot after
// sync mode observed the snapshotted document reach a terminal state.
// Without this, a future tick would re-resolve the same document via
// (kbID, knowledgeID) just to find it has already finished.
func (s *KnowledgeScheduledRebuildScheduler) clearLastTriggered(
	ctx context.Context,
	kb *types.KnowledgeBase,
	now time.Time,
) {
	if kb == nil || kb.ScheduledRebuildConfig == nil {
		return
	}
	kb.ScheduledRebuildConfig.ClearLastTriggered()
	kb.UpdatedAt = now
	if err := s.kbRepo.UpdateKnowledgeBase(ctx, kb); err != nil {
		logger.Warnf(ctx,
			"[scheduled_rebuild] clear_last_triggered persist_failed: kb=%s err=%v",
			kb.ID, err,
		)
	}
}

// lookupLastTriggeredStatus reads the snapshotted document's
// parse_status. Returns ("", false) when the row has been deleted /
// soft-deleted so the caller can treat "the document is gone" as
// terminal and unblock the next trigger.
func (s *KnowledgeScheduledRebuildScheduler) lookupLastTriggeredStatus(
	ctx context.Context,
	kb *types.KnowledgeBase,
) (string, bool) {
	if kb == nil || kb.ScheduledRebuildConfig == nil {
		return "", false
	}
	id := kb.ScheduledRebuildConfig.LastTriggeredKnowledgeID
	if id == "" {
		return "", false
	}
	kg, err := s.kgService.GetRepository().GetKnowledgeByID(ctx, kb.TenantID, id)
	if err != nil || kg == nil {
		return "", false
	}
	// Defensive: if the row belongs to a different KB (shouldn't
	// happen because IDs are globally unique, but a tenant with a
	// schema migration could in theory reuse an ID), bail out
	// instead of trusting the status.
	if kg.KnowledgeBaseID != kb.ID {
		return "", false
	}
	return kg.ParseStatus, true
}

// validParseStatuses is the canonical enum of every parse_status a
// Knowledge row can carry. Used by resolveStatuses to filter the
// operator-configured allowlist so a typo doesn't brick the scheduler
// (e.g. an env-var paste that drops one of the trailing 'd's).
var validParseStatuses = map[string]struct{}{
	types.ParseStatusUnprocessed: {}, // DB DDL 默认态；外部脚本入库、从未入队的文档
	types.ParseStatusPending:     {},
	types.ParseStatusUploaded:    {},
	types.ParseStatusProcessing:  {},
	types.ParseStatusFinalizing:  {},
	types.ParseStatusCompleted:   {},
	types.ParseStatusFailed:      {},
	types.ParseStatusDeleting:    {},
	types.ParseStatusCancelled:   {},
}

// resolveStatuses validates and deduplicates the per-KB
// Statuses allowlist. Empty list (operator cleared it) or every entry
// invalid falls back to ["failed"] so the scheduler never silently
// does nothing. Unknown entries are dropped with a warning so a typo
// doesn't brick the scheduler.
func (s *KnowledgeScheduledRebuildScheduler) resolveStatuses(ctx context.Context, raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, status := range raw {
		status = strings.TrimSpace(status)
		if status == "" {
			continue
		}
		if _, ok := validParseStatuses[status]; !ok {
			logger.Warnf(ctx,
				"[scheduled_rebuild] resolve_statuses drop_unknown: status=%q valid=%v",
				status,
				[]string{
					types.ParseStatusUnprocessed, types.ParseStatusPending, types.ParseStatusUploaded,
					types.ParseStatusProcessing, types.ParseStatusFinalizing,
					types.ParseStatusCompleted, types.ParseStatusFailed,
					types.ParseStatusDeleting, types.ParseStatusCancelled,
				},
			)
			continue
		}
		if _, dup := seen[status]; dup {
			continue
		}
		seen[status] = struct{}{}
		out = append(out, status)
	}
	if len(out) == 0 {
		return []string{types.ParseStatusFailed}
	}
	return out
}

// tickKB picks up to batch_size candidates in the given KB (ordered
// oldest-first) and calls ReparseKnowledge on each. Returns the
// number actually repaired (may be < batch_size if fewer candidates
// exist).
//
// Errors are logged but do not block subsequent candidates — a single
// bad row shouldn't starve the rest of the queue.
//
// Sync-mode contract: at most one candidate is processed per tick
// (batch_size is clamped to 1 inside the loop). We refuse to start if
// the document we triggered in a previous tick is still in a non-
// terminal parse_status — the operator asked for serial rebuilds and
// skipping the next trigger is the only way to honour that. A
// terminal status (completed / failed / cancelled / deleting) or a
// deleted document both clear the snapshot and let this tick proceed.
//
// Async-mode contract: every tick submits up to batch_size rebuilds
// without consulting the snapshot. The "上次触发的文档" panel in the KB
// editor shows whatever happened most recently, but it is not used to
// gate the next tick.
func (s *KnowledgeScheduledRebuildScheduler) tickKB(
	ctx context.Context,
	kb *types.KnowledgeBase,
	now time.Time,
) (int, error) {
	cfg := kb.ScheduledRebuildConfig
	mode := cfg.ModeOrDefault()
	batchSize := s.resolveBatchSize(cfg.BatchSize)
	statuses := s.resolveStatuses(ctx, cfg.Statuses)

	// Per-KB tick entry log. Always log so operators can see exactly
	// what knobs the scheduler is honouring at the moment of the call
	// (UI changes take effect on the next poll without a restart).
	logger.Infof(ctx,
		"[scheduled_rebuild] tickKB step enter: kb=%s mode=%s batch=%d interval=%ds statuses=%v last_triggered_id=%s",
		kb.ID, mode, batchSize,
		cfg.IntervalSeconds,
		statuses,
		cfg.LastTriggeredKnowledgeID,
	)

	kbCtx := context.WithValue(ctx, types.TenantIDContextKey, kb.TenantID)

	// Sync mode gating. Done before we ask the data layer for
	// candidates so we don't waste a query on a KB we already know
	// to skip this tick.
	if mode == types.ScheduledRebuildModeSync {
		if prevStatus, ok := s.lookupLastTriggeredStatus(kbCtx, kb); ok {
			if s.isInFlightNonTerminal(prevStatus) {
				logger.Infof(ctx,
					"[scheduled_rebuild] tickKB skip sync_mode_gate: kb=%s last_triggered_knowledge=%s status=%q",
					kb.ID, kb.ScheduledRebuildConfig.LastTriggeredKnowledgeID, prevStatus,
				)
				return 0, nil
			}
			// The previous triggered document is now in a
			// terminal state (completed / failed / cancelled /
			// deleting) or has been deleted — drop the snapshot
			// so the next tick (now this tick) is free to pick a
			// fresh candidate.
			s.clearLastTriggered(kbCtx, kb, now)
		}
	}

	candidates, err := s.kgService.GetRepository().ListKnowledgeForRepair(
		kbCtx, kb.TenantID, kb.ID,
		statuses,
		time.Time{},
		batchSize,
	)
	if err != nil {
		return 0, err
	}

	// Candidate-count log. This is the single most useful line when
	// operators report "the scheduler is running but never triggers":
	// a 0 here means the statuses allowlist doesn't match any document
	// in this KB, so the most likely fix is to widen the allowlist.
	logger.Infof(ctx,
		"[scheduled_rebuild] tickKB step candidates: kb=%s candidates=%d statuses=%v batch_size=%d",
		kb.ID, len(candidates), statuses, batchSize,
	)

	if len(candidates) == 0 {
		return 0, nil
	}

	var repaired int
	for _, k := range candidates {
		if s.stopped.Load() {
			break
		}
		// Per-candidate dispatch log. Tells the operator which
		// specific document is being rebuilt this tick — the KB id
		// alone is not enough when investigating "why did doc X get
		// picked and not doc Y".
		logger.Infof(ctx,
			"[scheduled_rebuild] tickKB step dispatch: kb=%s knowledge=%s file=%q current_parse_status=%q",
			kb.ID, k.ID, k.FileName, k.ParseStatus,
		)
		// ReparseKnowledge reads *types.Tenant from
		// TenantInfoContextKey; without it the downstream
		// `ctx.Value(...).(*types.Tenant)` assertion panics.
		// Mirror the bootstrap the asynq worker performs for queued
		// tasks: hydrate the Tenant here.
		callCtx, err := s.withTenantInfo(kbCtx, kb.TenantID)
		if err != nil {
			logger.Warnf(ctx,
				"[scheduled_rebuild] tickKB step tenant_load_failed: tenant_id=%d kb=%s knowledge=%s err=%v",
				kb.TenantID, k.KnowledgeBaseID, k.ID, err,
			)
			continue
		}
		// KB config is the single source of truth for scheduled rebuilds —
		// never honour overrides persisted on the document metadata from
		// earlier uploads, and never let a (currently impossible) caller
		// override slip through. This prevents a stale per-document override
		// (e.g. question_generation_config.enabled=true from a previous KB
		// config) from resurrecting work the operator explicitly disabled
		// in the KB editor. See ReparseKnowledge doc comment for the
		// exact contract.
		if _, rerr := s.kgService.ReparseKnowledge(callCtx, k.ID, nil, false, true); rerr != nil {
			logger.Warnf(ctx,
				"[scheduled_rebuild] tickKB step reparse_failed: kb=%s knowledge=%s err=%v",
				k.KnowledgeBaseID, k.ID, rerr,
			)
			continue
		}
		repaired++
		// Sync mode only cares about the first candidate; treat
		// the rest as over-quota and stop iterating.
		if mode == types.ScheduledRebuildModeSync {
			s.recordLastTriggered(kbCtx, kb, k, now)
			break
		}
		if repaired >= batchSize {
			break
		}
	}

	return repaired, nil
}

// withTenantInfo returns a child context that carries both
// TenantIDContextKey (already set by the caller) and the fully hydrated
// TenantInfoContextKey. Mirrors the bootstrap the asynq worker performs
// before invoking ReparseKnowledge from a queued task.
func (s *KnowledgeScheduledRebuildScheduler) withTenantInfo(ctx context.Context, tenantID uint64) (context.Context, error) {
	tenant, err := s.tenantRepo.GetTenantByID(ctx, tenantID)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, types.TenantInfoContextKey, tenant), nil
}
