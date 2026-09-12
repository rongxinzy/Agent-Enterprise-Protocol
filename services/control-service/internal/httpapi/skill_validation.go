package httpapi

import (
	"net/http"
	"regexp"
	"strings"
)

var (
	skillIdentifierPattern        = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9])?$`)
	skillVersionIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._+-]{0,126}[A-Za-z0-9])?$`)
)

func validSkillIdentifier(value string) bool {
	return skillIdentifierPattern.MatchString(value)
}

func validSkillVersionIdentifier(value string) bool {
	return skillVersionIdentifierPattern.MatchString(value)
}

func requireSkillIdentifier(response http.ResponseWriter, request *http.Request, value string) bool {
	if validSkillIdentifier(value) {
		return true
	}
	writeProblem(response, request, http.StatusBadRequest, "INVALID_SKILL_ID", "The Skill identifier is invalid.")
	return false
}

func requireSkillVersionIdentifier(response http.ResponseWriter, request *http.Request, value string) bool {
	if validSkillVersionIdentifier(value) {
		return true
	}
	writeProblem(response, request, http.StatusBadRequest, "INVALID_SKILL_VERSION", "The Skill version identifier is invalid.")
	return false
}

func skillObjectKey(skillID, version, digest string) (string, bool) {
	if !validSkillIdentifier(skillID) || !validSkillVersionIdentifier(version) {
		return "", false
	}
	return strings.Join([]string{"skills", skillID, version, digest + ".zip"}, "/"), true
}
