// Package jobs provides backend-neutral durable job and schedule contracts,
// workers, and schedulers. Database adapters such as platform/pgjobs
// provide the actual store implementation.
package jobs

import (
	"github.com/plinth-go/plinth/platform/jobs/jobutil"
)

const maxIdentifierBytes = jobutil.MaxIdentifierBytes

func validateIdentifier(value, label string) error {
	return jobutil.ValidateIdentifier(value, label)
}
