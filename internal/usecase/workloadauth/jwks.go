package workloadauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
)

type verificationKey struct {
	kid, alg string
	public   any
}

// jwk holds the RFC 7517/7518 members Gordon reads; pointers record presence.
type jwk struct {
	Kty    string    `json:"kty"`
	Kid    string    `json:"kid"`
	Alg    string    `json:"alg"`
	Use    *string   `json:"use"`
	KeyOps *[]string `json:"key_ops"`
	N      string    `json:"n"`
	E      string    `json:"e"`
	Crv    string    `json:"crv"`
	X      string    `json:"x"`
	Y      string    `json:"y"`
}

// isSigningKey: use, when present, must be "sig"; key_ops, when present, must allow "verify".
func (k jwk) isSigningKey() bool {
	return (k.Use == nil || *k.Use == "sig") && (k.KeyOps == nil || slices.Contains(*k.KeyOps, "verify"))
}

// parseJWKS keeps only signing keys usable by RS256 (RSA ≥ 2048 bits) or ES256 (P-256).
func parseJWKS(raw []byte) ([]verificationKey, error) {
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, err
	}
	var keys []verificationKey
	seen := make(map[string]bool)
	for _, k := range set.Keys {
		public, alg := parsePublicKey(k)
		if !k.isSigningKey() || public == nil || (k.Alg != "" && k.Alg != alg) {
			continue
		}
		if k.Kid != "" && seen[k.Kid] {
			return nil, fmt.Errorf("ambiguous JWKS kid")
		}
		seen[k.Kid] = true
		keys = append(keys, verificationKey{k.Kid, alg, public})
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no usable signing keys")
	}
	return keys, nil
}

func decodeB64(raw string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil
	}
	return b
}

func parsePublicKey(k jwk) (any, string) {
	switch k.Kty {
	case "RSA":
		n, e := new(big.Int).SetBytes(decodeB64(k.N)), new(big.Int).SetBytes(decodeB64(k.E))
		if n.BitLen() < 2048 || n.BitLen() > 8192 || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 || e.Bit(0) == 0 {
			return nil, ""
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, "RS256"
	case "EC":
		x, y := decodeB64(k.X), decodeB64(k.Y)
		if k.Crv != "P-256" || len(x) != 32 || len(y) != 32 {
			return nil, ""
		}
		// ParseUncompressedPublicKey rejects points that are not on the curve.
		public, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			return nil, ""
		}
		return public, "ES256"
	}
	return nil, ""
}
