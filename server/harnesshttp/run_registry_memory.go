package harnesshttp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryRunRegistry is an in-process RunRegistry useful for tests and embedded
// single-binary deployments that still want the registry API surface.
type MemoryRunRegistry struct {
	mu      sync.Mutex
	clock   func() time.Time
	records map[string]runRegistryRecord
}

type runRegistryRecord struct {
	info  RunInfo
	token string
}

func NewMemoryRunRegistry() *MemoryRunRegistry {
	return newMemoryRunRegistryWithClock(time.Now)
}

func newMemoryRunRegistryWithClock(clock func() time.Time) *MemoryRunRegistry {
	if clock == nil {
		clock = time.Now
	}
	return &MemoryRunRegistry{clock: clock, records: make(map[string]runRegistryRecord)}
}

func (r *MemoryRunRegistry) Claim(ctx context.Context, claim RunClaim) (RunLease, error) {
	if err := ctx.Err(); err != nil {
		return RunLease{}, err
	}
	if err := validateRunClaim(claim); err != nil {
		return RunLease{}, err
	}
	if r == nil {
		return RunLease{}, fmt.Errorf("memory run registry is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if record, ok := r.records[claim.RunID]; ok && !claimable(record.info, r.clock().UTC()) {
		return RunLease{}, ErrRunClaimed
	}
	token := randomLeaseToken()
	info := RunInfo{
		RunID: claim.RunID, Status: claim.Status, OwnerID: claim.OwnerID,
		LeaseUntil: timePtr(claim.LeaseUntil), StartedAt: claim.StartedAt,
		UpdatedAt: claim.UpdatedAt,
	}
	r.records[claim.RunID] = runRegistryRecord{info: info, token: token}
	return RunLease{RunID: claim.RunID, OwnerID: claim.OwnerID, Token: token}, nil
}

func (r *MemoryRunRegistry) Update(ctx context.Context, lease RunLease, info RunInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("memory run registry is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.recordForLease(lease)
	if err != nil {
		return err
	}
	info.RunID = lease.RunID
	info.OwnerID = lease.OwnerID
	if info.StartedAt.IsZero() {
		info.StartedAt = record.info.StartedAt
	}
	r.records[lease.RunID] = runRegistryRecord{info: cloneRunInfo(info), token: lease.Token}
	return nil
}

func (r *MemoryRunRegistry) Release(ctx context.Context, lease RunLease, info RunInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("memory run registry is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.recordForLease(lease)
	if err != nil {
		return err
	}
	if !terminal(info.Status) {
		return fmt.Errorf("release status must be terminal")
	}
	info.RunID = lease.RunID
	info.OwnerID = lease.OwnerID
	if info.StartedAt.IsZero() {
		info.StartedAt = record.info.StartedAt
	}
	info.LeaseUntil = nil
	r.records[lease.RunID] = runRegistryRecord{info: cloneRunInfo(info), token: ""}
	return nil
}

func (r *MemoryRunRegistry) Get(ctx context.Context, runID string) (RunInfo, error) {
	if err := ctx.Err(); err != nil {
		return RunInfo{}, err
	}
	if r == nil {
		return RunInfo{}, fmt.Errorf("memory run registry is nil")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return RunInfo{}, ErrInvalidRunID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[runID]
	if !ok {
		return RunInfo{}, ErrRunNotFound
	}
	return cloneRunInfo(record.info), nil
}

// List returns all registry records sorted by updatedAt descending, then runID.
func (r *MemoryRunRegistry) List(ctx context.Context) ([]RunInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("memory run registry is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RunInfo, 0, len(r.records))
	for _, record := range r.records {
		out = append(out, cloneRunInfo(record.info))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].RunID < out[j].RunID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (r *MemoryRunRegistry) recordForLease(lease RunLease) (runRegistryRecord, error) {
	if strings.TrimSpace(lease.RunID) == "" || strings.TrimSpace(lease.OwnerID) == "" || strings.TrimSpace(lease.Token) == "" {
		return runRegistryRecord{}, ErrRunLeaseLost
	}
	record, ok := r.records[lease.RunID]
	if !ok {
		return runRegistryRecord{}, ErrRunNotFound
	}
	if terminal(record.info.Status) || record.token != lease.Token || record.info.OwnerID != lease.OwnerID {
		return runRegistryRecord{}, ErrRunLeaseLost
	}
	return record, nil
}

func validateRunClaim(claim RunClaim) error {
	if strings.TrimSpace(claim.RunID) == "" {
		return ErrInvalidRunID
	}
	if strings.TrimSpace(claim.OwnerID) == "" {
		return fmt.Errorf("run owner ID is required")
	}
	if claim.Status == "" {
		return fmt.Errorf("run status is required")
	}
	if terminal(claim.Status) {
		return fmt.Errorf("claim status cannot be terminal")
	}
	if claim.LeaseUntil.IsZero() {
		return fmt.Errorf("run leaseUntil is required")
	}
	if claim.StartedAt.IsZero() {
		return fmt.Errorf("run startedAt is required")
	}
	if claim.UpdatedAt.IsZero() {
		return fmt.Errorf("run updatedAt is required")
	}
	return nil
}

func claimable(info RunInfo, now time.Time) bool {
	if terminal(info.Status) {
		return true
	}
	if info.LeaseUntil == nil {
		return false
	}
	return !info.LeaseUntil.After(now)
}

func cloneRunInfo(info RunInfo) RunInfo {
	if info.LeaseUntil != nil {
		info.LeaseUntil = timePtr(*info.LeaseUntil)
	}
	return info
}

func timePtr(t time.Time) *time.Time {
	tt := t.UTC()
	return &tt
}

var _ RunRegistry = (*MemoryRunRegistry)(nil)
