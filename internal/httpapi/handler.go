// Package httpapi exposes the service over HTTP with the same envelope as
// token-holder ({"success", "data"} / {"success", "code", "message"}).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"birdeye.so/lp-holder/internal/domain"
	"birdeye.so/lp-holder/internal/service"
)

const maxBodyBytes = 4 << 10

// HoldersService is what the handler needs from the service layer.
type HoldersService interface {
	Holders(ctx context.Context, q service.Query) (*service.Page, error)
}

// Handler serves the liquidity-provider endpoints.
type Handler struct {
	svc HoldersService
}

// NewHandler creates a Handler.
func NewHandler(svc HoldersService) *Handler {
	return &Handler{svc: svc}
}

// Routes registers the endpoints on a new mux.
//
//	POST /v1/holder/liquidity-provider  JSON body (Request)
//	GET  /v1/holder/liquidity-provider  ?address=&offset=&limit=&group_by=
//	GET  /healthz
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/holder/liquidity-provider", h.post)
	mux.HandleFunc("GET /v1/holder/liquidity-provider", h.get)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, Envelope{Success: true, Data: "ok"})
	})
	return mux
}

func (h *Handler) post(w http.ResponseWriter, r *http.Request) {
	var req Request
	// Unknown fields are ignored on purpose so older clients keep working.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&req); err != nil || dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be a single JSON object")
		return
	}
	h.serve(w, r, req)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := Request{Address: q.Get("address"), GroupBy: q.Get("group_by")}
	var err error
	if req.Offset, err = optionalInt(q.Get("offset")); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_offset", "offset must be an integer")
		return
	}
	if req.Limit, err = optionalInt(q.Get("limit")); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be an integer")
		return
	}
	h.serve(w, r, req)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request, req Request) {
	address := strings.TrimSpace(req.Address)
	if address == "" {
		address = strings.TrimSpace(req.PoolAddress)
	}
	if address == "" {
		writeError(w, http.StatusBadRequest, "address_required", "address is required")
		return
	}
	groupBy := service.GroupBy(strings.ToLower(strings.TrimSpace(req.GroupBy)))
	if groupBy != "" && groupBy != service.GroupByPosition && groupBy != service.GroupByWallet {
		writeError(w, http.StatusBadRequest, "invalid_group_by", "group_by must be position or wallet")
		return
	}
	if req.Offset < 0 || req.Limit < 0 {
		writeError(w, http.StatusBadRequest, "invalid_pagination", "offset and limit must be >= 0")
		return
	}

	page, err := h.svc.Holders(r.Context(), service.Query{
		Pool: address, Offset: req.Offset, Limit: req.Limit, GroupBy: groupBy,
	})
	if err != nil {
		status, code, msg := mapError(err)
		if status >= http.StatusInternalServerError {
			slog.Error("liquidity provider request failed", "pool", address, "error", err)
		}
		writeError(w, status, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{Success: true, Data: NewPageResponse(page)})
}

// mapError turns service errors into HTTP responses without leaking internals.
func mapError(err error) (status int, code, msg string) {
	switch {
	case errors.Is(err, domain.ErrInvalidAddress):
		return http.StatusBadRequest, "invalid_address", "address must be a base58 Solana public key"
	case errors.Is(err, domain.ErrPoolNotFound):
		return http.StatusNotFound, "pool_not_found", "pool account not found"
	case errors.Is(err, domain.ErrUnsupportedPool):
		return http.StatusUnprocessableEntity, "unsupported_pool", "address is not a pool of a supported protocol"
	case errors.Is(err, domain.ErrPoolTooLarge):
		return http.StatusUnprocessableEntity, "pool_too_large", "pool has too many positions for on-demand computation"
	case errors.Is(err, domain.ErrBusy):
		return http.StatusServiceUnavailable, "busy", "server is computing too many pools, retry shortly"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout", "computation timed out, retry later"
	case errors.Is(err, context.Canceled):
		return 499, "client_closed_request", "client closed request"
	default:
		return http.StatusBadGateway, "upstream_error", "failed to read pool data from chain"
	}
}

func optionalInt(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	return strconv.Atoi(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, Envelope{Success: false, Code: code, Message: msg})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Warn("write response failed", "error", err)
	}
}
