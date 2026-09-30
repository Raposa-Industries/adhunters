package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Raposa-Industries/adhunters/launch/internal/actions"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
)

// requests lists other services' requests, the waiting ones first.
func (a *API) requests(w http.ResponseWriter, r *http.Request) {
	list, err := a.l.Store().Requests(r.Context(), 100)
	if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]any{"requests": list})
}

func requestID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		say(w, http.StatusBadRequest, "id inválido")
		return 0, false
	}
	return id, true
}

func (a *API) request(w http.ResponseWriter, r *http.Request) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	req, err := a.l.Store().Request(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]any{"request": req})
}

// decide confirms or refuses a request. Only a signed-in person can: the
// requesting service never confirms its own ask.
func (a *API) decide(w http.ResponseWriter, r *http.Request) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	person := Who(r)
	if person == "" {
		say(w, http.StatusForbidden, "só uma pessoa conectada pode decidir um pedido")
		return
	}
	var (
		req  store.Request
		done []actions.Done
		err  error
	)
	if strings.HasSuffix(r.URL.Path, "/confirm") {
		req, done, err = a.l.Confirm(r.Context(), person, id)
	} else {
		req, err = a.l.Refuse(r.Context(), person, id)
	}
	switch {
	case actions.IsDecided(err):
		say(w, http.StatusConflict, "este pedido já foi decidido; recarregue a página")
		return
	case errors.Is(err, store.ErrNotFound):
		say(w, http.StatusNotFound, "pedido não encontrado")
		return
	case err != nil:
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]any{"request": req, "done": done})
}
