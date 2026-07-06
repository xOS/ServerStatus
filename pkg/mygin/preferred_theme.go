package mygin

import "github.com/gin-gonic/gin"

const frontendTemplate = "theme-default"

func PreferredTheme(c *gin.Context) {
}

func GetPreferredTheme(c *gin.Context, path string) string {
	return frontendTemplate + path
}
