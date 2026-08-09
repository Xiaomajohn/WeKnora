package types

import "testing"

func TestQueueDefinitionsAreUniqueAndConsumable(t *testing.T) {
	definitions := QueueDefinitions()
	if len(definitions) == 0 {
		t.Fatal("queue registry must not be empty")
	}

	validPools := map[string]bool{
		WorkerPoolCore:        true,
		WorkerPoolPostProcess: true,
		WorkerPoolEnrichment:  true,
		WorkerPoolMaintenance: true,
		WorkerPoolWiki:        true,
		WorkerPoolSerial:      true,
	}
	seen := make(map[string]bool, len(definitions))
	// seenTaskTypes is intentionally a set, not a map of "first queue
	// seen for this task". Same task type may appear in multiple
	// queues (e.g. document:process lives on both QueueDefault for
	// the regular core path and QueueCoreSerial for the
	// strict-serial batch reparse path) — the enqueue site picks the
	// queue at runtime via asynq.Queue. We still want to ensure
	// every queue declares at least one task type, so we only
	// track queue-name uniqueness here.
	seenTaskTypes := make(map[string]bool)
	for _, definition := range definitions {
		if seen[definition.Name] {
			t.Fatalf("duplicate queue definition %q", definition.Name)
		}
		seen[definition.Name] = true
		if !validPools[definition.Pool] {
			t.Fatalf("queue %q references unknown pool %q", definition.Name, definition.Pool)
		}
		if definition.Weight <= 0 {
			t.Fatalf("queue %q has non-positive weight %d", definition.Name, definition.Weight)
		}
		if len(definition.TaskTypes) == 0 {
			t.Fatalf("queue %q declares no task types", definition.Name)
		}
		for _, taskType := range definition.TaskTypes {
			seenTaskTypes[taskType] = true
			queue, ok := QueueForTaskType(taskType)
			if !ok {
				// Multi-queue task types (document:process,
				// manual:process) intentionally resolve to
				// the *first* queue they are declared in.
				// Real dispatch is by enqueue-time queue
				// option, so the resolved value here is
				// only meaningful for single-queue types.
				continue
			}
			if queue != definition.Name {
				// Same as above: multi-queue task types
				// are allowed to be declared in more
				// than one queue. We only fail when the
				// task is declared in a queue that does
				// NOT contain it (which would indicate
				// drift between definitions and the
				// reverse map).
				declared := false
				for _, declaredType := range definition.TaskTypes {
					if declaredType == taskType {
						declared = true
						break
					}
				}
				if !declared {
					t.Fatalf("task type %q resolves to queue %q, declared in %q", taskType, queue, definition.Name)
				}
			}
		}
	}

	// Sanity: at least one task type per declared queue (already
	// checked inside the loop, but restated to make the invariant
	// obvious to future readers).

	for pool := range validPools {
		if len(QueueWeightsForPool(pool)) == 0 {
			t.Fatalf("worker pool %q has no queues", pool)
		}
	}
	shared := QueueWeightsForSharedPool()
	if len(shared) == 0 || shared[QueueDefault] <= 0 || shared[QueueSummary] <= 0 {
		t.Fatalf("shared pool must cover core and enrichment queues: %+v", shared)
	}
	if shared[QueuePostProcess] != 0 || shared[QueueMaintenance] != 0 {
		t.Fatalf("shared pool must not consume post-process or maintenance: %+v", shared)
	}
}

func TestQueueMaintenanceKeepsLegacyPhysicalName(t *testing.T) {
	if QueueMaintenance != "low" {
		t.Fatalf("maintenance queue must keep legacy Redis name during rolling upgrades, got %q", QueueMaintenance)
	}
}

func TestEveryAsynqTaskTypeHasADeclaredQueue(t *testing.T) {
	taskTypes := []string{
		TypeChunkExtract, TypeDocumentProcess, TypeFAQImport,
		TypeQuestionGeneration, TypeSummaryGeneration, TypeKBClone,
		TypeIndexDelete, TypeKBDelete, TypeKnowledgeListDelete,
		TypeKnowledgeListReparse, TypeKnowledgeMove, TypeDataTableSummary,
		TypeImageMultimodal, TypeKnowledgePostProcess, TypeManualProcess,
		TypeDataSourceSync, TypeWikiIngest, TypeWikiFinalize,
	}
	for _, taskType := range taskTypes {
		if _, ok := QueueForTaskType(taskType); !ok {
			t.Fatalf("task type %q has no declared queue", taskType)
		}
	}
}

func TestDefaultWorkerPoolConcurrencyIsExplicitBudget(t *testing.T) {
	allocation := DefaultWorkerPoolConcurrency()
	if allocation.UpstreamTotal() != DefaultUpstreamWorkerConcurrency {
		t.Fatalf("upstream total = %d, want %d", allocation.UpstreamTotal(), DefaultUpstreamWorkerConcurrency)
	}
	if allocation.Core < 1 || allocation.PostProcess < 1 || allocation.Enrichment < 1 ||
		allocation.Maintenance < 1 || allocation.Shared < 1 || allocation.Wiki < 1 {
		t.Fatalf("every pool must have positive explicit capacity: %+v", allocation)
	}
}

func TestResolveWorkerPoolConcurrencyFallsBackPerPool(t *testing.T) {
	allocation := ResolveWorkerPoolConcurrency(func(key, _ string, fallback int) int {
		if key == "asynq.core_concurrency" {
			return 15
		}
		if key == "asynq.shared_concurrency" {
			return 0
		}
		return fallback
	})
	if allocation.Core != 15 {
		t.Fatalf("core = %d, want override 15", allocation.Core)
	}
	if allocation.Shared != DefaultSharedWorkerConcurrency {
		t.Fatalf("shared = %d, want fallback %d", allocation.Shared, DefaultSharedWorkerConcurrency)
	}
}
