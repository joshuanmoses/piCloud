// Package health defines the vocabulary every subsystem uses to describe its
// own condition.
//
// It exists so that "healthy" means exactly one thing across the database,
// storage, and API layers. Each package defining its own status strings would
// let them drift silently, and the frontend would start receiving a status it
// does not recognize.
package health

// Status describes the condition of a subsystem.
type Status string

const (
	// Healthy means the subsystem is fully operational.
	Healthy Status = "healthy"
	// Degraded means the subsystem works but needs administrator attention —
	// a disk approaching full, a slow database. Traffic should keep flowing.
	Degraded Status = "degraded"
	// Unhealthy means the subsystem cannot serve requests correctly.
	Unhealthy Status = "unhealthy"
)

// String implements fmt.Stringer.
func (s Status) String() string { return string(s) }

// OK reports whether the subsystem can serve traffic. Degraded counts as OK:
// a nearly-full disk is a warning for the operator, not a reason for an
// orchestrator to pull the instance out of service.
func (s Status) OK() bool { return s != Unhealthy }

// severity orders statuses so they can be combined.
func (s Status) severity() int {
	switch s {
	case Unhealthy:
		return 2
	case Degraded:
		return 1
	default:
		return 0
	}
}

// Worst returns the most severe of the given statuses. An empty list is
// Healthy, which is correct: nothing is broken.
func Worst(statuses ...Status) Status {
	worst := Healthy
	for _, s := range statuses {
		if s.severity() > worst.severity() {
			worst = s
		}
	}
	return worst
}
