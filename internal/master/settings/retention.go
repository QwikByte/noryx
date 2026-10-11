package settings

import (
	"net/http"
	"time"

	"github.com/QwikByte/noryx/internal/master/httpapi"
)

const (
	minUsageDays      = 30
	maxUsageDays      = 365
	maxSeenDays       = 365
	maxOperationHours = 24
)

// Retention tells how long the master keeps what it records besides the log: the averages of
// each hour of usage, beyond the week of minutes; where players played, which it can also
// stop recording, e.g. for privacy; and finished operations, which live in memory.
type Retention struct {
	UsageDays      int  `json:"usageDays"`
	SeenPlayers    bool `json:"seenPlayers"`
	SeenDays       int  `json:"seenDays"`
	OperationHours int  `json:"operationHours"`
}

func (r Retention) validate() error {
	switch {
	case r.UsageDays < minUsageDays || r.UsageDays > maxUsageDays:
		return httpapi.Errorf(http.StatusBadRequest, "Enter how long the usage of each hour is kept, from %d to %d days.", minUsageDays, maxUsageDays)
	case r.SeenDays < 1 || r.SeenDays > maxSeenDays:
		return httpapi.Errorf(http.StatusBadRequest, "Enter how long players seen are kept, from 1 to %d days.", maxSeenDays)
	case r.OperationHours < 1 || r.OperationHours > maxOperationHours:
		return httpapi.Errorf(http.StatusBadRequest, "Enter how long finished operations are listed, from 1 to %d hours.", maxOperationHours)
	}
	return nil
}

// UsageRetention implements usage.Config.
func (s *Service) UsageRetention() time.Duration {
	return time.Duration(s.Get().Retention.UsageDays) * 24 * time.Hour
}

// SeenPlayers implements player.Config.
func (s *Service) SeenPlayers() bool { return s.Get().Retention.SeenPlayers }

// SeenRetention implements player.Config.
func (s *Service) SeenRetention() time.Duration {
	return time.Duration(s.Get().Retention.SeenDays) * 24 * time.Hour
}

// OperationsKept implements operation.Config.
func (s *Service) OperationsKept() time.Duration {
	return time.Duration(s.Get().Retention.OperationHours) * time.Hour
}
