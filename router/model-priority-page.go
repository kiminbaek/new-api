// model-priority-page.go — [CUSTOM] legacy compatibility redirect.
package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RegisterModelPriorityPage preserves old bookmarks while keeping the model
// priority board inside the authenticated React admin console.
func RegisterModelPriorityPage(router *gin.Engine) {
	router.GET("/admin/model-priority", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Redirect(http.StatusTemporaryRedirect, "/model-priority")
	})
}
