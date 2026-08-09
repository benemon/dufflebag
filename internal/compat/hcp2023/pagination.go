package hcp2023

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"

	"github.com/benemon/dufflebag/internal/compat/hcp2023/models"
)

const defaultPageSize = 100

func paginationPage(
	r *http.Request,
	total int,
) (int, int, *models.HashicorpCloudCommonPaginationResponse, error) {
	query := r.URL.Query()
	nextToken := query.Get("pagination.next_page_token")
	previousToken := query.Get("pagination.previous_page_token")
	if nextToken != "" && previousToken != "" {
		return 0, 0, nil, fmt.Errorf("next_page_token and previous_page_token cannot both be set")
	}

	pageSize := defaultPageSize
	if raw := query.Get("pagination.page_size"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return 0, 0, nil, fmt.Errorf("pagination.page_size must be a non-negative integer")
		}
		if parsed > 0 {
			if parsed > int64(^uint(0)>>1) {
				return 0, 0, nil, fmt.Errorf("pagination.page_size is too large")
			}
			pageSize = int(parsed)
		}
	}

	start := 0
	if token := nextToken + previousToken; token != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return 0, 0, nil, fmt.Errorf("invalid pagination token")
		}
		parsed, err := strconv.Atoi(string(decoded))
		if err != nil || parsed < 0 || parsed > total {
			return 0, 0, nil, fmt.Errorf("invalid pagination token")
		}
		start = parsed
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	response := &models.HashicorpCloudCommonPaginationResponse{}
	if end < total {
		response.NextPageToken = encodePageToken(end)
	}
	if start > 0 {
		previousStart := start - pageSize
		if previousStart < 0 {
			previousStart = 0
		}
		response.PreviousPageToken = encodePageToken(previousStart)
	}
	return start, end, response, nil
}

func encodePageToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}
