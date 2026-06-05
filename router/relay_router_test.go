package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRelayRouterRegistersRootAliasesWithoutModelsPageConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetRelayRouter(engine)
	SetVideoRouter(engine)

	routes := make(map[string]bool)
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = true
	}

	for _, route := range []string{
		http.MethodPost + " /chat/completions",
		http.MethodPost + " /responses",
		http.MethodPost + " /videos",
		http.MethodGet + " /v1/models",
	} {
		if !routes[route] {
			t.Fatalf("expected route %s to be registered", route)
		}
	}
	if routes[http.MethodGet+" /models"] {
		t.Fatal("GET /models must remain available for the frontend route")
	}
}
