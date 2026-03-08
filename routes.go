package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// setupRoutes 设置路由配置
func setupRoutes(appServer *AppServer) *gin.Engine {
	// 设置 Gin 模式
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	// 添加中间件
	router.Use(errorHandlingMiddleware())
	router.Use(corsMiddleware())

	// 健康检查
	router.GET("/health", healthHandler)

	// MCP 端点 - 使用官方 SDK 的 Streamable HTTP Handler
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			return appServer.mcpServer
		},
		&mcp.StreamableHTTPOptions{
			JSONResponse: true, // 支持 JSON 响应
		},
	)
	// 兼容层：部分 MCP 客户端会先发起 GET /mcp 探测，如果未带会话时返回 text/plain，
	// 会导致客户端因 Content-Type 非 application/json 报错。
	mcpCompatHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.Header.Get("Mcp-Session-Id") == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"GET requires an active session"}}`))
			return
		}

		// 兼容部分客户端：官方 handler 在某些错误场景（如 session not found）会返回 text/plain。
		// 这里将 POST 的 text/plain 错误统一转换为 JSON-RPC 错误，避免客户端因 Content-Type 报协议错误。
		if r.Method == http.MethodPost {
			rec := httptest.NewRecorder()
			mcpHandler.ServeHTTP(rec, r)

			if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
				msg := strings.TrimSpace(rec.Body.String())
				if msg == "" {
					msg = "MCP request failed"
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(rec.Code)
				_, _ = w.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","error":{"code":-32000,"message":%q}}`, msg)))
				return
			}

			for k, vals := range rec.Header() {
				for _, v := range vals {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}

		mcpHandler.ServeHTTP(w, r)
	})

	router.Any("/mcp", gin.WrapH(mcpCompatHandler))
	router.Any("/mcp/*path", gin.WrapH(mcpCompatHandler))

	// API 路由组
	api := router.Group("/api/v1")
	{
		api.GET("/login/status", appServer.checkLoginStatusHandler)
		api.GET("/login/qrcode", appServer.getLoginQrcodeHandler)
		api.DELETE("/login/cookies", appServer.deleteCookiesHandler)
		api.POST("/publish", appServer.publishHandler)
		api.POST("/publish_video", appServer.publishVideoHandler)
		api.POST("/publish_article", appServer.publishArticleHandler)
		api.GET("/feeds/list", appServer.listFeedsHandler)
		api.GET("/feeds/search", appServer.searchFeedsHandler)
		api.POST("/feeds/search", appServer.searchFeedsHandler)
		api.POST("/feeds/detail", appServer.getFeedDetailHandler)
		api.POST("/user/profile", appServer.userProfileHandler)
		api.POST("/feeds/comment", appServer.postCommentHandler)
		api.POST("/feeds/comment/reply", appServer.replyCommentHandler)
		api.GET("/user/me", appServer.myProfileHandler)
	}

	return router
}
