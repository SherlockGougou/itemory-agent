package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"rsc.io/qr"
)

// PairPayloadScheme is the URL scheme the app registers for pairing links.
const PairPayloadScheme = "itemory://pair"

// pairPayload builds the string encoded into the pairing QR code. The app
// parses it back into a base URL, the one-time code and the server identity.
func pairPayload(baseURL, code, serverID string, apiVersion int) string {
	query := url.Values{}
	query.Set("u", baseURL)
	query.Set("t", code)
	query.Set("s", serverID)
	query.Set("v", fmt.Sprintf("%d", apiVersion))
	return PairPayloadScheme + "?" + query.Encode()
}

// qrSVG renders text as a scalable SVG QR code (error correction level M).
func qrSVG(text string) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const (
		quiet = 4 // modules of margin, required by the QR spec
		scale = 8 // output pixels per module
		ink   = "#111111"
	)
	side := (code.Size + 2*quiet) * scale

	var out strings.Builder
	fmt.Fprintf(&out,
		`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img">`,
		side, side, side, side)
	fmt.Fprintf(&out, `<rect width="%d" height="%d" fill="#ffffff"/>`, side, side)
	fmt.Fprintf(&out, `<path fill="%s" d="`, ink)
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; {
			if !code.Black(x, y) {
				x++
				continue
			}
			start := x
			for x < code.Size && code.Black(x, y) {
				x++
			}
			width := (x - start) * scale
			fmt.Fprintf(&out, "M%d %dh%dv%dh-%dz",
				(start+quiet)*scale, (y+quiet)*scale, width, scale, width)
		}
	}
	out.WriteString(`"/></svg>`)
	return out.String(), nil
}

// handleClaimQR renders the currently armed pairing code as a QR code.
func (s *Server) handleClaimQR(w http.ResponseWriter, r *http.Request) {
	code, armed := s.d.Tokens.ClaimCode()
	if !armed {
		writeError(w, http.StatusConflict, "claim_not_armed", "start pairing from the dashboard first")
		return
	}
	payload := pairPayload(pairingBaseURL(r), code, s.d.Tokens.ServerID(), s.d.APIVersion)
	svg, err := qrSVG(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "qr_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(svg))
}
