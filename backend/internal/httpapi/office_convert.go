package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/office"
)

// officeConvertToPDF asks the Document Server conversion service to render the
// current document as PDF, then downloads the result. Conversion is synchronous
// from the user's perspective; the timeout stays below the nginx API budget.
func (s *Server) officeConvertToPDF(ctx context.Context, runtime officeRuntime, format office.Format, title, contentURL string) ([]byte, error) {
	endpoint := strings.TrimRight(runtime.PublicURL, "/") + "/converter"
	request := map[string]any{
		"async":      false,
		"filetype":   format.Ext,
		"outputtype": "pdf",
		"key":        newID("oconv"),
		"title":      title,
		"url":        contentURL,
	}
	token, err := office.SignJWT(runtime.Secret, request, 10*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("could not sign the conversion request")
	}
	request["token"] = token
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("could not encode the conversion request")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	deadline := time.Now().Add(50 * time.Second)
	var fileURL string
	for {
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("could not create the conversion request")
		}
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest.Header.Set("Accept", "application/json")
		response, err := client.Do(httpRequest)
		if err != nil {
			return nil, fmt.Errorf("OnlyOffice conversion is not reachable: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("could not read the conversion response")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("OnlyOffice conversion returned HTTP %d", response.StatusCode)
		}
		var result struct {
			Error      int    `json:"error"`
			EndConvert bool   `json:"endConvert"`
			Percent    int    `json:"percent"`
			FileURL    string `json:"fileUrl"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("OnlyOffice returned an invalid conversion response")
		}
		if result.Error != 0 {
			return nil, fmt.Errorf("OnlyOffice conversion failed with error %d", result.Error)
		}
		if result.FileURL != "" && result.EndConvert {
			fileURL = result.FileURL
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("OnlyOffice conversion timed out")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	pdf, _, err := s.officeFetchURL(ctx, officeDocumentFetchURL(fileURL, runtime.PublicURL), officeDocumentMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("could not download the converted PDF: %w", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, fmt.Errorf("OnlyOffice returned an invalid PDF")
	}
	return pdf, nil
}
