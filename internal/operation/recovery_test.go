package operation

import (
	"testing"
	"time"
)

func TestRecoveryMetadataRejectsUnboundedOrUnclassifiedState(t *testing.T) {
	for _, r := range []Recovery{{Attempts: 4, NotBefore: time.Now(), Health: HealthHealthy}, {Attempts: 1, Health: HealthHealthy}, {Health: "raw diagnostic"}} {
		if r.Validate() == nil {
			t.Fatal("unsafe recovery metadata accepted")
		}
	}
}
