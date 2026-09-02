package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestLegacyModelPriorityPageRedirectsToAdminConsole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	RegisterModelPriorityPage(engine)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/model-priority", nil)
	engine.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusTemporaryRedirect, recorder.Code)
	assert.Equal(t, "/admin/model-priority", recorder.Header().Get("Location"))
	assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
}
