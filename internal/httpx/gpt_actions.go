package httpx

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/auth"
	"github.com/uvwt/agentdock/internal/buildinfo"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/mcp"
)

const gptActionBodyLimit = 1 << 20

func registerGPTActionsRoutes(mux *http.ServeMux, server *mcp.Server, cfg config.Config, oauthStore *auth.OAuthStore) {
	mux.HandleFunc("/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, gptActionsOpenAPISchema(cfg, r))
	})

	mux.HandleFunc("/privacy", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		baseURL := html.EscapeString(requestPublicBaseURL(cfg, r))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><html><head><meta charset='utf-8'><title>AgentDock GPT Actions Privacy</title></head><body><h1>AgentDock GPT Actions Privacy</h1><p>This endpoint is a self-hosted interface to the user's AgentDock instance.</p><p>Requests are processed by the configured AgentDock server at <code>"+baseURL+"</code>. AgentDock does not add an external analytics or advertising service through this adapter.</p><p>Tool calls may read or modify resources that the AgentDock operator has explicitly exposed. Access is protected by the same AgentDock Bearer/OAuth authentication used by the server.</p></body></html>")
	})

	mux.HandleFunc("/gpt/status", gptStatusHandler(server, cfg, oauthStore))
	mux.HandleFunc("/gpt/tools", gptToolsHandler(server, cfg, oauthStore))
	mux.HandleFunc("/gpt/call", gptCallHandler(server, cfg, oauthStore))
}

func gptActionAuthorized(w http.ResponseWriter, r *http.Request, cfg config.Config, oauthStore *auth.OAuthStore) bool {
	if !cfg.AuthRequired() {
		return true
	}
	staticOK := cfg.AuthToken != "" && (auth.Bearer{Token: cfg.AuthToken}).Authorized(r)
	oauthOK := authorizedOAuth(r, cfg, oauthStore)
	if staticOK || oauthOK {
		return true
	}
	setBearerChallenge(w, cfg, r, strings.TrimSpace(r.Header.Get("Authorization")) != "")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func gptStatusHandler(server *mcp.Server, cfg config.Config, oauthStore *auth.OAuthStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !gptActionAuthorized(w, r, cfg, oauthStore) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		envelope := server.CallToolDirect(ctx, "server_info", map[string]any{})
		writeJSON(w, map[string]any{
			"ok":     !toolEnvelopeIsError(envelope),
			"result": envelope,
		})
	}
}

func gptToolsHandler(server *mcp.Server, cfg config.Config, oauthStore *auth.OAuthStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !gptActionAuthorized(w, r, cfg, oauthStore) {
			return
		}
		tools := server.ToolDescriptors()
		writeJSON(w, map[string]any{
			"ok":    true,
			"count": len(tools),
			"tools": tools,
		})
	}
}

type gptToolCallRequest struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

func gptCallHandler(server *mcp.Server, cfg config.Config, oauthStore *auth.OAuthStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !gptActionAuthorized(w, r, cfg, oauthStore) {
			return
		}
		var request gptToolCallRequest
		body := http.MaxBytesReader(w, r.Body, gptActionBodyLimit)
		if err := decodeSingleJSON(body, &request); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON request: " + err.Error()})
			return
		}
		request.Tool = strings.TrimSpace(request.Tool)
		if request.Tool == "" {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "tool is required"})
			return
		}
		envelope := server.CallToolDirect(r.Context(), request.Tool, request.Arguments)
		writeJSON(w, map[string]any{
			"ok":     !toolEnvelopeIsError(envelope),
			"tool":   request.Tool,
			"result": envelope,
		})
	}
}

func toolEnvelopeIsError(envelope map[string]any) bool {
	value, _ := envelope["isError"].(bool)
	return value
}

func gptActionsOpenAPISchema(cfg config.Config, r *http.Request) map[string]any {
	baseURL := requestPublicBaseURL(cfg, r)
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	}

	unauthorizedResponse := map[string]any{
		"description": "Missing or invalid AgentDock Bearer token",
	}

	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "AgentDock GPT Actions",
			"description": "Direct OpenAPI adapter for the same tools exposed by AgentDock MCP. No bridge process is required.",
			"version":     buildinfo.Version,
		},
		"servers": []map[string]any{{"url": baseURL}},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"BearerAuth": map[string]any{
					"type":         "http",
					"scheme":       "bearer",
					"bearerFormat": "AgentDock token",
				},
			},
			"schemas": map[string]any{
				"ToolCallRequest": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"tool"},
					"properties": map[string]any{
						"tool": map[string]any{
							"type":        "string",
							"description": "Exact AgentDock tool name returned by listAgentDockTools.",
						},
						"arguments": map[string]any{
							"type":                 "object",
							"description":          "Arguments for the selected AgentDock tool.",
							"properties":           map[string]any{},
							"additionalProperties": true,
						},
					},
				},
				"ActionResponse": map[string]any{
					"type":                 "object",
					"additionalProperties": true,
					"properties": map[string]any{
						"ok": map[string]any{
							"type":        "boolean",
							"description": "Whether the AgentDock request completed successfully.",
						},
						"tool": map[string]any{
							"type":        "string",
							"description": "Tool name for a tool-call response.",
						},
						"count": map[string]any{
							"type":        "integer",
							"description": "Number of tools returned by listAgentDockTools.",
						},
						"tools": map[string]any{
							"type":        "array",
							"description": "Currently available AgentDock tools.",
							"items": map[string]any{
								"type":                 "object",
								"additionalProperties": true,
								"properties": map[string]any{
									"name":        map[string]any{"type": "string"},
									"title":       map[string]any{"type": "string"},
									"description": map[string]any{"type": "string"},
								},
							},
						},
						"result": map[string]any{
							"description": "Structured AgentDock result. Shape depends on the selected tool.",
						},
						"error": map[string]any{
							"type":        "string",
							"description": "Error message when the Action request itself is invalid.",
						},
					},
				},
			},
		},
		"paths": map[string]any{
			"/gpt/status": map[string]any{
				"get": map[string]any{
					"operationId": "getAgentDockStatus",
					"x-openai-isConsequential": false,
					"summary":     "Get AgentDock status and server information",
					"security":    []map[string]any{{"BearerAuth": []any{}}},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "AgentDock status response",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{"$ref": "#/components/schemas/ActionResponse"},
								},
							},
						},
						"401": unauthorizedResponse,
					},
				},
			},
			"/gpt/tools": map[string]any{
				"get": map[string]any{
					"operationId": "listAgentDockTools",
					"x-openai-isConsequential": false,
					"summary":     "List all tools currently available in AgentDock",
					"security":    []map[string]any{{"BearerAuth": []any{}}},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "AgentDock tool catalog",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{"$ref": "#/components/schemas/ActionResponse"},
								},
							},
						},
						"401": unauthorizedResponse,
					},
				},
			},
			"/gpt/call": map[string]any{
				"post": map[string]any{
					"operationId": "callAgentDockTool",
					"x-openai-isConsequential": false,
					"summary":     "Call any currently available AgentDock tool",
					"description": "Use listAgentDockTools first when the exact tool name or argument schema is unknown.",
					"security":    []map[string]any{{"BearerAuth": []any{}}},
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{"$ref": "#/components/schemas/ToolCallRequest"},
							},
						},
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "AgentDock tool call response",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{"$ref": "#/components/schemas/ActionResponse"},
								},
							},
						},
						"400": map[string]any{"description": "Invalid Action request"},
						"401": unauthorizedResponse,
					},
				},
			},
		},
	}
}
