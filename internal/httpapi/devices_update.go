package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// PATCH изменяет только явно переданные метаданные, не выдавая профиль и его секреты.
func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name          json.RawMessage `json:"name"`
		PreferredExit json.RawMessage `json:"preferred_exit"`
	}
	if !decode(w, r, &input) {
		return
	}
	optionalString := func(value json.RawMessage) (*string, bool) {
		if value == nil {
			return nil, true
		}
		var text *string
		if json.Unmarshal(value, &text) != nil || text == nil {
			return nil, false
		}
		return text, true
	}
	name, nameValid := optionalString(input.Name)
	exit, exitValid := optionalString(input.PreferredExit)
	if !nameValid || !exitValid {
		fail(w, http.StatusBadRequest, "device metadata fields must be strings")
		return
	}
	device, err := s.store.UpdateDevice(r.Context(), chi.URLParam(r, "id"), name, exit)
	if err != nil {
		s.error(w, err)
		return
	}
	respond(w, http.StatusOK, device)
}
