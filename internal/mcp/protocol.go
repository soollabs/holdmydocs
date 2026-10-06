package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpProtocolVersion = "2026-07-28"

const mcpGateBodyLimit = 1 << 20

type mcpGateRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Meta            map[string]json.RawMessage `json:"_meta"`
	} `json:"params"`
}

func writeMCPGateError(w http.ResponseWriter, code int, id json.RawMessage, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    any    `json:"data,omitempty"`
		} `json:"error"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Error: struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    any    `json:"data,omitempty"`
		}{Code: code, Message: message, Data: data},
	})
}

// mcpProtocolGate applies the additional request contract introduced by the
// modern protocol only. Older Streamable HTTP requests are passed through to
// the SDK, which handles their version negotiation and transport lifecycle.
// The body is re-buffered so the SDK handler can read it after inspection.
func mcpProtocolGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			if r.Header.Get("MCP-Protocol-Version") >= mcpProtocolVersion {
				w.Header().Set("Allow", http.MethodPost)
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpGateBodyLimit))
		if err != nil {
			writeMCPGateError(w, -32600, nil, "invalid request", nil)
			return
		}
		var request mcpGateRequest
		if err := json.Unmarshal(body, &request); err != nil {
			writeMCPGateError(w, -32600, nil, "invalid request", nil)
			return
		}
		headerVersion := r.Header.Get("MCP-Protocol-Version")
		// A request is modern when it declares the modern version in either
		// transport metadata location. An initialise request without those
		// fields, or with an older negotiated version, belongs to legacy
		// Streamable HTTP and must be left to the SDK.
		_, hasModernClientInfo := request.Params.Meta["io.modelcontextprotocol/clientInfo"]
		_, hasModernCapabilities := request.Params.Meta["io.modelcontextprotocol/clientCapabilities"]
		modern := headerVersion >= mcpProtocolVersion ||
			request.Params.Meta["io.modelcontextprotocol/protocolVersion"] != nil ||
			hasModernClientInfo || hasModernCapabilities
		if !modern {
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if headerVersion != mcpProtocolVersion {
			writeMCPGateError(w, sdk.CodeUnsupportedProtocolVersion, request.ID, "unsupported protocol version", sdk.UnsupportedProtocolVersionData{
				Supported: []string{mcpProtocolVersion},
				Requested: headerVersion,
			})
			return
		}
		if r.Header.Get("Mcp-Session-Id") != "" {
			writeMCPGateError(w, sdk.CodeHeaderMismatch, request.ID, "session headers are unsupported", nil)
			return
		}
		if r.URL.Query().Get("sessionId") != "" {
			writeMCPGateError(w, sdk.CodeHeaderMismatch, request.ID, "session query parameters are unsupported", nil)
			return
		}
		if len(request.Params.Meta) == 0 {
			writeMCPGateError(w, sdk.CodeHeaderMismatch, request.ID, "missing request metadata", nil)
			return
		}
		if len(request.Params.Meta["io.modelcontextprotocol/protocolVersion"]) == 0 {
			writeMCPGateError(w, sdk.CodeHeaderMismatch, request.ID, "missing metadata protocol version", nil)
			return
		}
		if len(request.Params.Meta["io.modelcontextprotocol/clientInfo"]) == 0 {
			writeMCPGateError(w, sdk.CodeHeaderMismatch, request.ID, "missing client info", nil)
			return
		}
		if len(request.Params.Meta["io.modelcontextprotocol/clientCapabilities"]) == 0 {
			writeMCPGateError(w, sdk.CodeMissingRequiredClientCapabilities, request.ID, "missing client capabilities", nil)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

// mcpBaseURLKey carries the request-scoped base URL used to build absolute
// upload URLs in tool output.
type mcpBaseURLKey struct{}

func newMCPHTTPHandler(baseURL string, serverForRequest func(*http.Request) *sdk.Server) http.Handler {
	modern := sdk.NewStreamableHTTPHandler(serverForRequest, &sdk.StreamableHTTPOptions{
		Stateless:                    true,
		PropagateRequestCancellation: true,
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBaseURL := baseURL
		if requestBaseURL == "" {
			requestBaseURL = "http://" + r.Host
			if r.TLS != nil {
				requestBaseURL = "https://" + r.Host
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), mcpBaseURLKey{}, requestBaseURL))
		mcpProtocolGate(modern).ServeHTTP(w, r)
	})
	return http.NewCrossOriginProtection().Handler(handler)
}
