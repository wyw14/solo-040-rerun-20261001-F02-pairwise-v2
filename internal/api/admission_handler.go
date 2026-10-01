package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type admissionActionInput struct {
	VesselID string `json:"vessel_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func parseOptionalVessel(value string) (uuid.UUID, error) {
	if value == "" {
		return uuid.Nil, nil
	}
	return uuid.Parse(value)
}

// allowNextVesselHandler clears the FIFO head vessel to enter the chamber.
func allowNextVesselHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chamberID := chi.URLParam(r, "chamberID")
		decision, err := runtime.CycleService.AllowNext(r.Context(), chamberID)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		if !decision.Allowed {
			writeError(w, http.StatusConflict, errors.New(decision.Reason))
			return
		}
		writeJSON(w, http.StatusOK, decision)
	}
}

func vesselEntryHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input admissionActionInput
		if r.ContentLength != 0 {
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		vesselID, err := parseOptionalVessel(input.VesselID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		chamberID := chi.URLParam(r, "chamberID")
		entry, err := runtime.CycleService.ConfirmEntry(r.Context(), chamberID, vesselID)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	}
}

func vesselWithdrawHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input admissionActionInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		vesselID, err := parseOptionalVessel(input.VesselID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		chamberID := chi.URLParam(r, "chamberID")
		reason := input.Reason
		if reason == "" {
			reason = "vessel withdrew before entering"
		}
		entry, err := runtime.CycleService.Withdraw(r.Context(), chamberID, vesselID, reason)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	}
}

func vesselLostHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input admissionActionInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		vesselID, err := parseOptionalVessel(input.VesselID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		chamberID := chi.URLParam(r, "chamberID")
		entry, err := runtime.CycleService.MarkLost(r.Context(), chamberID, vesselID, input.Reason)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, entry)
	}
}
