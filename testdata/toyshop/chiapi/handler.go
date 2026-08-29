// Package chiapi is the chi fixture: a route registered inside a
// Route("/auth", ...) group (the entrypoint id must carry the prefix), a
// request DTO read via json.NewDecoder(r.Body).Decode, and a response sent
// through a hand-rolled respondJSON helper.
package chiapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	minio "github.com/minio/minio-go/v7"
	"golang.org/x/crypto/bcrypt"

	"example.com/toyshop/model"
	"example.com/toyshop/pkg/log"
	"example.com/toyshop/service"
)

type Handler struct {
	svc    *service.Service
	log    *log.Logger
	files  *minio.Client
	jwtKey []byte
}

func Register(r chi.Router, s *service.Service, l *log.Logger) {
	h := &Handler{svc: s, log: l}
	r.Route("/auth", func(r chi.Router) {
		r.Post("/login", h.Login)
	})
	// The products routes feed the entity shelf: /products plus SQL on the
	// products table must shelve these flows under "product".
	r.Get("/products", h.ListProducts)
	r.Post("/products", h.CreateProduct)
}

type loginReq struct {
	Identifier string `json:"identifier" validate:"required"`
	Password   string `json:"password" validate:"required,min=8"`
}

type loginResp struct {
	Token      string `json:"token"`
	OrderID    string `json:"order_id"`
	ReceiptURL string `json:"receipt_url"`
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	o, err := h.findOrderByIdentifier(r.Context(), req.Identifier)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	// Security-effect fixture: the guard consumes a third-party check -
	// the bcrypt call must surface as "verifies password", never filtered.
	if err := bcrypt.CompareHashAndPassword([]byte(o.Item), []byte(req.Password)); err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": o.ID}).SignedString(h.jwtKey)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "token"})
		return
	}
	receipt, _ := h.files.PresignedGetObject(r.Context(), "receipts", o.ID+".pdf", 15*time.Minute, nil)
	respondJSON(w, http.StatusOK, toLoginResp(o, token, receipt.String()))
}

// toLoginResp is the DTO-mapper fixture: a pure transform that must fold
// into its caller as "+N mapping", not stand as a step.
func toLoginResp(o *model.Order, token, receiptURL string) loginResp {
	return loginResp{Token: token, OrderID: o.ID, ReceiptURL: receiptURL}
}

// findOrderByIdentifier is the either/or fixture: an email identifier takes
// the tail-return path, anything else falls through to the phone lookup.
// Flow maps must render one branch decision with exclusive yes/no children -
// never both lookups as unconditional siblings.
func (h *Handler) findOrderByIdentifier(ctx context.Context, identifier string) (*model.Order, error) {
	if strings.Contains(identifier, "@") {
		return h.svc.GetOrder(ctx, identifier)
	}
	o, err := h.svc.FindByPhone(ctx, identifier)
	if err != nil {
		return nil, err
	}
	return o, nil
}

type createProductReq struct {
	Name  string `json:"name" validate:"required"`
	Price int    `json:"price" validate:"required,min=1"`
}

func (h *Handler) ListProducts(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListProducts(r.Context())
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "listing products"})
		return
	}
	respondJSON(w, http.StatusOK, items)
}

func (h *Handler) CreateProduct(w http.ResponseWriter, r *http.Request) {
	var req createProductReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	p, err := h.svc.CreateProduct(r.Context(), req.Name, req.Price)
	if err != nil {
		respondJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusCreated, p)
}

func respondJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
