package domain

import "time"

// BlockerRef is the normalized blocker reference attached to one issue.
type BlockerRef struct {
	ID         string
	Identifier string
	State      string
}

// Issue is the normalized issue model shared by tracker, prompt, and orchestration layers.
type Issue struct {
	ID               string
	Identifier       string
	Title            string
	Description      string
	Priority         *int
	State            string
	BranchName       string
	URL              string
	AssigneeID       string
	BlockedBy        []BlockerRef
	Labels           []string
	AssignedToWorker bool
	CreatedAt        *time.Time
	UpdatedAt        *time.Time
}

// LabelNames returns the normalized issue labels.
func (i Issue) LabelNames() []string {
	return append([]string(nil), i.Labels...)
}
