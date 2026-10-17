package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/zatto-dev/shy-garlic/internal/config"
	"github.com/zatto-dev/shy-garlic/internal/health"
	"github.com/zatto-dev/shy-garlic/internal/i18n"
	"github.com/zatto-dev/shy-garlic/internal/store"
)

// Everything the API says about a failure goes through here, so that
//   - the wording is translated (EN/PL, picked from the request),
//   - nothing from the inside leaks: no SQL, no file paths, no parser internals,
//   - the mapping from a domain error to an HTTP code and a message exists once.

// errorTable maps the sentinel errors of config, store and health to an HTTP code
// and an i18n key. The first match wins; wrapped errors match through errors.Is.
var errorTable = []struct {
	target error
	code   int
	key    string
}{
	{config.ErrServiceNotFound, http.StatusNotFound, "error_service_not_found"},
	{config.ErrGroupNotFound, http.StatusNotFound, "error_group_not_found"},
	{health.ErrNoSuchService, http.StatusNotFound, "error_service_not_found"},
	{config.ErrDuplicate, http.StatusConflict, "error_name_duplicate"},
	{config.ErrInheritedField, http.StatusBadRequest, "error_field_inherited"},
	{config.ErrUnsupportedLayout, http.StatusBadRequest, "error_file_layout"},
	{config.ErrNameRequired, http.StatusBadRequest, "invalid_name"},
	{config.ErrPosition, http.StatusBadRequest, "error_move"},
	{config.ErrTitleEmpty, http.StatusBadRequest, "error_title_empty"},
	{config.ErrTitleTooLong, http.StatusBadRequest, "error_title_long"},
	{config.ErrTitleChars, http.StatusBadRequest, "error_title_chars"},
	{config.ErrChangedOnDisk, http.StatusConflict, "file_conflict"},
	{store.ErrUserExists, http.StatusConflict, "error_user_exists"},
	{store.ErrLoginShort, http.StatusBadRequest, "error_login_short"},
	{store.ErrPasswordShort, http.StatusBadRequest, "error_password_short"},
	{store.ErrVersionNotFound, http.StatusNotFound, "error_version_not_found"},
	{store.ErrTokenNotFound, http.StatusNotFound, "error_token_not_found"},
	{store.ErrTokenName, http.StatusBadRequest, "error_token_name"},
	{store.ErrTooManyTokens, http.StatusConflict, "error_token_limit"},
}

// classify reports the HTTP code and message key for a known error.
func classify(err error) (code int, key string, ok bool) {
	for _, e := range errorTable {
		if errors.Is(err, e.target) {
			return e.code, e.key, true
		}
	}
	return 0, "", false
}

// apiFail answers a JSON error with a translated message.
func apiFail(w http.ResponseWriter, r *http.Request, code int, key string) {
	jsonError(w, code, i18n.T(i18n.FromRequest(r), key))
}

// apiError answers a JSON error for err: a known error gets its own code and
// message; anything else is an internal failure — the detail goes to the log,
// the client gets a generic 500.
func apiError(w http.ResponseWriter, r *http.Request, err error) {
	if code, key, ok := classify(err); ok {
		apiFail(w, r, code, key)
		return
	}
	slog.Error("api request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	apiFail(w, r, http.StatusInternalServerError, "error_internal")
}

// plainError is the text/plain twin of apiFail, for the non-JSON endpoints.
func plainError(w http.ResponseWriter, r *http.Request, code int, key string) {
	http.Error(w, i18n.T(i18n.FromRequest(r), key), code)
}

// plainInternal logs err and answers a generic, translated 500 as plain text.
func plainInternal(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	plainError(w, r, http.StatusInternalServerError, "error_internal")
}
