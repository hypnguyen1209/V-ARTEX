package notify

// Snapshot is the notification_events.snapshot JSONB contract, written in the
// finding transaction by db and read by server delivery/filtering. It belongs here
// as a notification payload; db serializes it without interpreting fields.
// Store finding fields at event time: later edits must not alter the notification's
// severity or conclusions. This also avoids joins across findings/tasks/assets at render time.
type Snapshot struct {
	// Event kind: finding_created / finding_status_changed.
	Kind      string  `json:"kind"`
	FindingID int64   `json:"finding_id"`
	TaskID    int64   `json:"task_id"`
	VulnClass string  `json:"vulnclass"`
	Name      string  `json:"name"`
	Severity  string  `json:"severity"`
	Summary   string  `json:"summary"`
	AssetIDs  []int64 `json:"asset_ids"`
	// Set only for kind=finding_status_changed.
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
}

// Item is a finding ready for channel rendering.
type Item struct {
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets contains resolved display names, such as domains or IPs, supplied by
	// server because this package does not access the database.
	Assets []string
	// DetailURL links to the finding; omit it when public_base_url is unset.
	DetailURL string
	// Status-change fields render as Pending → Fixed when both are nonempty.
	FromStatus string
	ToStatus   string
}

// IsStatusChange reports whether this item is a status-change event.
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title prefers the user-assigned name, then vulnclass, then a placeholder;
// never return an empty title.
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "(Unnamed vulnerability)"
}

// Message holds the complete content of one channel delivery.
type Message struct {
	// Single delivery contains one item; a digest contains the batch.
	// Callers must provide at least one item.
	Items []Item
	// Batch renders digest text with a different title, time window and item count.
	Batch bool
	// WindowMinutes supplies the digest period for text such as the last N minutes.
	// Pass configuration explicitly rather than using time.Since so rendering stays deterministic.
	WindowMinutes int
	// HomeURL is the platform URL from public_base_url; omit the entry link when empty.
	HomeURL string
}
