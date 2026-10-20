package httpapi

import "net/http"

func (s *Server) jwtVerifierConfiguration(response http.ResponseWriter, request *http.Request) {
	configuration, err := s.app.Tokens.VerifierConfiguration()
	if err != nil {
		writeProblem(response, request, http.StatusServiceUnavailable, "JWT_DISCOVERY_UNAVAILABLE", "JWT verification discovery is unavailable.")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, configuration)
}
