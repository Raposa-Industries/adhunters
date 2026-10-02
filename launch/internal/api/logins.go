package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Raposa-Industries/adhunters/launch/internal/logins"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
)

// The Contas page: the network logins Launch uses. A secret goes in once,
// in a POST body, and never comes back out: no answer holds it, and no
// error or log line repeats a request body.

func (a *API) loginRoutes(m *http.ServeMux, p string) {
	m.HandleFunc("GET "+p+"logins", a.logins)
	m.HandleFunc("POST "+p+"logins/check", a.checkLogin)
	m.HandleFunc("POST "+p+"logins", a.addLogin)
	m.HandleFunc("GET "+p+"logins/{id}/allowed", a.loginAllowed)
	m.HandleFunc("PUT "+p+"logins/{id}", a.changeLogin)
	m.HandleFunc("DELETE "+p+"logins/{id}", a.removeLogin)
	m.HandleFunc("PUT "+p+"logins/{id}/proxy", a.loginProxy)
	m.HandleFunc("PUT "+p+"logins/{id}/user-id", a.loginUserID)
	m.HandleFunc("PUT "+p+"accounts/{net}/{account}/proxy", a.accountProxy)
}

func (a *API) loginsOn(w http.ResponseWriter) (*logins.Service, bool) {
	if a.Logins == nil {
		say(w, http.StatusServiceUnavailable, "as contas não estão ligadas neste servidor")
		return nil, false
	}
	return a.Logins, true
}

func loginID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		say(w, http.StatusBadRequest, "id inválido")
		return 0, false
	}
	return id, true
}

func (a *API) loginFail(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		say(w, http.StatusNotFound, "esse login não existe mais")
		return
	}
	a.fail(w, err)
}

func (a *API) logins(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loginsOn(w)
	if !ok {
		return
	}
	list, err := s.List(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]any{"logins": list})
}

// loginIn is a login as Nova conta sends it. The secret and the proxy (which
// may hold a password) are never logged or sent back.
type loginIn struct {
	Name         string   `json:"name"`
	ClientID     string   `json:"client_id"`
	UserID       string   `json:"user_id"`
	ClientSecret string   `json:"client_secret"`
	Proxy        string   `json:"proxy"`
	Accounts     []string `json:"accounts"`
}

func (a *API) checkLogin(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loginsOn(w)
	if !ok {
		return
	}
	var in loginIn
	if !read(w, r, &in) {
		return
	}
	list, err := s.Check(r.Context(), in.ClientID, in.ClientSecret, in.Proxy)
	if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]any{"accounts": list})
}

func (a *API) addLogin(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loginsOn(w)
	if !ok {
		return
	}
	var in loginIn
	if !read(w, r, &in) {
		return
	}
	id, err := s.Add(r.Context(), Who(r), in.Name, in.ClientID, in.UserID, in.ClientSecret, in.Proxy, in.Accounts)
	if err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusCreated, map[string]int64{"id": id})
}

func (a *API) loginAllowed(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loginsOn(w)
	if !ok {
		return
	}
	id, ok := loginID(w, r)
	if !ok {
		return
	}
	list, err := s.Allowed(r.Context(), id)
	if err != nil {
		a.loginFail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]any{"accounts": list})
}

func (a *API) changeLogin(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loginsOn(w)
	if !ok {
		return
	}
	id, ok := loginID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name     string   `json:"name"`
		Accounts []string `json:"accounts"`
	}
	if !read(w, r, &in) {
		return
	}
	if err := s.Change(r.Context(), id, in.Name, in.Accounts); err != nil {
		a.loginFail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) removeLogin(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loginsOn(w)
	if !ok {
		return
	}
	id, ok := loginID(w, r)
	if !ok {
		return
	}
	if err := s.Remove(r.Context(), id); err != nil {
		a.loginFail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]bool{"ok": true})
}

// loginProxy changes the proxy an added login's accounts go through. It is
// checked through the new proxy first (a read at Taboola).
func (a *API) loginProxy(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loginsOn(w)
	if !ok {
		return
	}
	id, ok := loginID(w, r)
	if !ok {
		return
	}
	var in struct {
		Proxy string `json:"proxy"`
	}
	if !read(w, r, &in) {
		return
	}
	if err := s.SetLoginProxy(r.Context(), id, in.Proxy); err != nil {
		a.loginFail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]bool{"ok": true})
}

// loginUserID changes an added login's Taboola user ID.
func (a *API) loginUserID(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loginsOn(w)
	if !ok {
		return
	}
	id, ok := loginID(w, r)
	if !ok {
		return
	}
	var in struct {
		UserID string `json:"user_id"`
	}
	if !read(w, r, &in) {
		return
	}
	if err := s.SetUserID(r.Context(), id, in.UserID); err != nil {
		a.loginFail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]bool{"ok": true})
}

// accountProxy sets or (with "") takes away the proxy of one account of the
// server's own login.
func (a *API) accountProxy(w http.ResponseWriter, r *http.Request) {
	s, ok := a.loginsOn(w)
	if !ok {
		return
	}
	if r.PathValue("net") != "taboola" {
		say(w, http.StatusNotFound, "rede desconhecida")
		return
	}
	var in struct {
		Proxy string `json:"proxy"`
	}
	if !read(w, r, &in) {
		return
	}
	if err := s.SetAccountProxy(r.Context(), Who(r), r.PathValue("account"), in.Proxy); err != nil {
		a.fail(w, err)
		return
	}
	send(w, http.StatusOK, map[string]bool{"ok": true})
}
