// Fixture: the bridge surface.
package api

import "net/http"

func writeJSONErr(w http.ResponseWriter, code int, msg string) {}

func serve(w http.ResponseWriter, r *http.Request) {
	writeJSONErr(w, http.StatusConflict, "that name is already taken")
	writeJSONErr(w, http.StatusBadRequest, "invalid_body")
	http.Error(w, "could not start the sign-in", http.StatusInternalServerError)
	http.Redirect(w, r, "/account?error=cancelled", http.StatusFound)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}
