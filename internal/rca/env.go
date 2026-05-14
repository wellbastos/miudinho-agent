package rca

import (
	"os"
	"strings"
)

// getenv é um helper local para testes do pacote rca.
// Nota: config.LoadFromEnv usa strings.TrimSpace; esta versão é intencional para testes.
func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
