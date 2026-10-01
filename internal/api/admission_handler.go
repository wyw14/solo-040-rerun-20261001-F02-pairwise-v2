package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

type admissionActionInput struct {
	VesselID string `json:"vessel_id"`
	Note     string `json:"note"`
}

// admissionCycle resolves the active, non-terminal transit for a chamber.
func admissionCycle(runtime *Runtime, chamberID string) (model.TransitCycle, error) {
	cycleValue, ok := runtime.Cycles.Current(chamberID)
	if !ok || cycleValue.Stage.Terminal() {
		return model.TransitCycle{}, errors.New("active transit not found")
	}
	return cycleValue, nil
}

func (in admissionActionInput) vessel() (uuid.UUID, error) {
	if in.VesselID == "" {
		return uuid.Nil, nil
	}
	return uuid.Parse(in.VesselID)
}

// admitNextHandler clears exactly one standing-by vessel: the current queue head.
func admitNextHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chamberID := chi.URLParam(r, "chamberID")
		cycleValue, err := admissionCycle(runtime, chamberID)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		decision, err := runtime.Admission.AdmitNext(r.Context(), cycleValue)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		if !decision.Allowed {
			writeError(w, http.StatusConflict, errors.New(decision.Reason))
			return
		}
		writeJSON(w, http.StatusAccepted, decision)
	}
}

func vesselEnteredHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chamberID := chi.URLParam(r, "chamberID")
		cycleValue, err := admissionCycle(runtime, chamberID)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		var input admissionActionInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		vesselID, err := input.vessel()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := runtime.Admission.ConfirmEntry(r.Context(), cycleValue, vesselID)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	}
}

func vesselWithdrawHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chamberID := chi.URLParam(r, "chamberID")
		cycleValue, err := admissionCycle(runtime, chamberID)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		var input admissionActionInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		vesselID, err := input.vessel()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := runtime.Admission.Withdraw(r.Context(), cycleValue, vesselID, input.Note)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	}
}

func vesselMissingHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chamberID := chi.URLParam(r, "chamberID")
		cycleValue, err := admissionCycle(runtime, chamberID)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		var input admissionActionInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		vesselID, err := input.vessel()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		entry, err := runtime.Admission.ReportMissing(r.Context(), cycleValue, vesselID, input.Note)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	}
}
