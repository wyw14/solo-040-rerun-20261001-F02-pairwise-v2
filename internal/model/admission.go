package model

// AdmissionState is the per-vessel confirmation state while a convoy enters a chamber.
type AdmissionState string

const (
	// AdmissionWaiting means the vessel is queued and standing by for clearance.
	AdmissionWaiting AdmissionState = "waiting"
	// AdmissionAdmitted means the vessel has been cleared (proceed signal) but has
	// not yet been observed entering the chamber.
	AdmissionAdmitted AdmissionState = "admitted"
	// AdmissionEntered means field staff confirmed the vessel physically entered.
	AdmissionEntered AdmissionState = "entered"
	// AdmissionWithdrawn means a previously granted clearance was pulled back. The
	// vessel did not enter and the convoy may continue with the next vessel.
	AdmissionWithdrawn AdmissionState = "withdrawn"
	// AdmissionFailed means the vessel missed its clearance or lost contact. The
	// convoy is blocked and no following vessel may be released.
	AdmissionFailed AdmissionState = "failed"
)

func (s AdmissionState) Valid() bool {
	switch s {
	case AdmissionWaiting, AdmissionAdmitted, AdmissionEntered, AdmissionWithdrawn, AdmissionFailed:
		return true
	default:
		return false
	}
}

// Resolved reports whether the vessel has finished the admission flow one way or
// another, so the queue cursor can advance to the next vessel.
func (s AdmissionState) Resolved() bool {
	return s == AdmissionEntered || s == AdmissionWithdrawn
}
