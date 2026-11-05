// Inbound Bearer authentication added in this fork.
package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

func bearerAuthentication(token string) echo.MiddlewareFunc {
	expected := sha256.Sum256([]byte(token))
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Health exposes only a static process-liveness response. CORS
			// middleware handles preflight before this middleware runs.
			if c.Request().Method == http.MethodGet && c.Request().URL.Path == "/healthz" {
				return next(c)
			}
			headers := c.Request().Header.Values(echo.HeaderAuthorization)
			if len(headers) == 1 {
				scheme, supplied, ok := strings.Cut(headers[0], " ")
				if ok && strings.EqualFold(scheme, "Bearer") && supplied != "" {
					actual := sha256.Sum256([]byte(supplied))
					if subtle.ConstantTimeCompare(actual[:], expected[:]) == 1 {
						return next(c)
					}
				}
			}
			c.Response().Header().Set("WWW-Authenticate", `Bearer realm="opencode-mcp-bridge"`)
			c.Response().Header().Set("Cache-Control", "no-store")
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		}
	}
}
