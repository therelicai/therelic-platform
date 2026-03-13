package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleListTransactions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"transactions": []any{},
		"count":        0,
		"message":      "metered transactions — coming with trust network launch",
	})
}

func (s *Server) handleTransactionSummary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"total_transactions": 0,
		"total_revenue":      "0.00",
		"period":             "current_month",
	})
}

func (s *Server) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
	_ = chi.URLParam(r, "txnID")
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "transaction details not yet available",
	})
}
