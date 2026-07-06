package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/Pruthviraj36/dotsync/internal/auth"
	"github.com/Pruthviraj36/dotsync/internal/model"
)

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response: {"error": msg}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func planLimitsFor(plan string) model.PlanLimits {
	limits, ok := model.Plans[plan]
	if !ok {
		return model.Plans["free"]
	}
	return limits
}

func effectivePlanForRequest(claims *auth.Claims, storedPlan string) string {
	if hasAdminFeatureOverride(claims) {
		return "business" // unlock all feature gates for server admins
	}
	if _, ok := model.Plans[storedPlan]; !ok {
		return "free"
	}
	return storedPlan
}

func hasAdminFeatureOverride(claims *auth.Claims) bool {
	if claims == nil {
		return false
	}
	return isServerAdminUsername(claims.Username)
}

func isServerAdminUsername(username string) bool {
	if username == "" {
		return false
	}
	raw := os.Getenv("DOTSYNC_ADMIN_USERS")
	if raw == "" {
		return false
	}
	normalized := normalizeUserHandle(username)
	for _, item := range strings.Split(raw, ",") {
		if normalizeUserHandle(strings.TrimSpace(item)) == normalized {
			return true
		}
	}
	return false
}

func normalizeUserHandle(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	return strings.TrimPrefix(v, "@")
}
